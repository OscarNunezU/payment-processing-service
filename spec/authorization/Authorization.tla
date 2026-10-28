--------------------------- MODULE Authorization ---------------------------
(***************************************************************************)
(* The authorization protocol of RFC 0001, section 5, for one payment.     *)
(*                                                                         *)
(* The platform writes every acquirer operation to its database before     *)
(* sending it, and resolves an uncertain outcome by asking the same        *)
(* acquirer: first an inquiry, then a reversal. In this model the API      *)
(* instance and the recovery worker run at the same time, both can crash,  *)
(* a lease can expire while its holder is still working, the card data can *)
(* expire, and the network loses, delays and repeats messages.             *)
(*                                                                         *)
(* Five constants each switch off one safeguard of the protocol. With any  *)
(* of them off, TLC must find a counterexample: that is how we know each   *)
(* safeguard is needed, and that the model is able to catch the mistake.   *)
(***************************************************************************)
EXTENDS Naturals, FiniteSets

CONSTANTS
    Acquirers,          \* the acquirers, for example {"A", "B"}
    MaxAttempts,        \* operations one payment may use: 2 allows one failover
    MaxCrashes,         \* crashes of the API and the worker, in total
    MaxInquiries,       \* unanswered inquiries before recovery reverses
    Duplicates,         \* the network may deliver the same request more than once
    \* The safeguards. All of them are TRUE in the real protocol.
    InquireOnTimeout,   \* a timeout is uncertain; FALSE treats it as "not sent" and fails over
    Tombstones,         \* an acquirer remembers a reversal for a key it has not seen yet
    WriteAheadReversal, \* the decision to reverse is recorded before the reversal is sent
    Fencing,            \* a decision only applies while its process holds the current lease epoch
    ReverseWhenCardGone \* "not found" without card data to resend is reversed, never just failed

ASSUME /\ Cardinality(Acquirers) >= 2
       /\ MaxAttempts \in Nat \ {0}
       /\ MaxInquiries \in Nat \ {0}
       /\ MaxCrashes \in Nat
       /\ {Duplicates, InquireOnTimeout, Tombstones, WriteAheadReversal, Fencing, ReverseWhenCardGone} \subseteq BOOLEAN

Keys == 1..MaxAttempts

OpStatuses == {"NONE", "SENDING", "UNKNOWN", "NOT_SENT", "REVERSING",
               "SUCCEEDED", "DECLINED", "REVERSED"}
Unresolved == {"SENDING", "UNKNOWN"}
\* Operations that may hold a live authorization: the unique index of
\* RFC 0001, section 7, allows at most one of them per payment.
MaybeLive == {"SENDING", "UNKNOWN", "REVERSING", "SUCCEEDED"}
Final == {"AUTHORIZED", "DECLINED", "FAILED"}

Messages == [kind : {"auth", "inq", "rev", "authResp", "inqResp", "revResp"},
             acq : Acquirers,
             key : Keys,
             result : {"none", "approved", "declined", "notfound", "reversed", "ok"}]

VARIABLES
    payment,  \* the payment's status in the database
    op,       \* op[k]: the operation with key k in the database; k is also the key the acquirer receives
    book,     \* book[a][k]: what acquirer a recorded for key k
    net,      \* messages in flight
    card,     \* whether the vault still has the card data to send an authorization
    api,      \* the API instance serving the merchant's request
    worker,   \* the recovery worker
    told,     \* the last thing the merchant was told
    crashes

vars == <<payment, op, book, net, card, api, worker, told, crashes>>

Msg(kind, a, k, r) == [kind |-> kind, acq |-> a, key |-> k, result |-> r]

Other(a) == CHOOSE b \in Acquirers : b /= a

\* The authorizations that are approved and not reversed, across all acquirers.
Live == {<<a, k>> \in Acquirers \X Keys : book[a][k] = "approved"}

\* What the API answers: the payment as the database has it.
Answer == IF payment \in Final THEN payment ELSE "PROCESSING"

\* A decision on operation k by the process holding epoch e. With fencing, a
\* process whose lease was taken over can no longer decide anything.
Holds(k, e) == ~Fencing \/ op[k].epoch = e

\* No other operation may hold a live authorization: the unique index.
IndexAllowsAnother(k) == \A j \in Keys \ {k} : op[j].status \notin MaybeLive

NewOp(a) == [acq |-> a, status |-> "SENDING", epoch |-> 1, lease |-> TRUE, inquiries |-> 0]

\* What an acquirer answered is a fact: whoever receives it records it, but
\* only while the operation is unresolved.
RecordFact(k, r) ==
    IF op[k].status \in Unresolved
    THEN /\ op' = [op EXCEPT ![k].status = IF r = "approved" THEN "SUCCEEDED" ELSE "DECLINED",
                             ![k].lease = FALSE]
         /\ payment' = IF payment = "PROCESSING"
                       THEN IF r = "approved" THEN "AUTHORIZED" ELSE "DECLINED"
                       ELSE payment
    ELSE UNCHANGED <<op, payment>>

AnswerAfter(p) == IF p \in Final THEN p ELSE "PROCESSING"

-----------------------------------------------------------------------------
(* The API instance *)

\* T1: the payment and its first operation are written before anything is sent.
ApiStart ==
    /\ api.pc = "start"
    /\ \E a \in Acquirers :
         /\ payment' = "PROCESSING"
         /\ op' = [op EXCEPT ![1] = NewOp(a)]
    /\ api' = [pc |-> "sending", key |-> 1, epoch |-> 1]
    /\ UNCHANGED <<book, net, card, worker, told, crashes>>

\* The acquirer refused the connection, so nothing left the platform: the
\* operation is NOT_SENT and the payment may fail over. Without the
\* InquireOnTimeout safeguard, a timeout is wrongly handled here too.
ApiNotSent ==
    LET k == api.key IN
    /\ UNCHANGED net
    /\ IF op[k].status = "SENDING" /\ Holds(k, api.epoch)
       THEN IF k < MaxAttempts /\ IndexAllowsAnother(k)
            THEN /\ op' = [op EXCEPT ![k].status = "NOT_SENT", ![k].lease = FALSE,
                                     ![k + 1] = NewOp(Other(op[k].acq))]
                 /\ api' = [pc |-> "sending", key |-> k + 1, epoch |-> 1]
                 /\ UNCHANGED <<payment, told>>
            ELSE /\ op' = [op EXCEPT ![k].status = "NOT_SENT", ![k].lease = FALSE]
                 /\ payment' = "FAILED"
                 /\ told' = "FAILED"
                 /\ api' = [api EXCEPT !.pc = "done"]
       ELSE \* recovery owns the operation now: answer what the database says
            /\ told' = Answer
            /\ api' = [api EXCEPT !.pc = "done"]
            /\ UNCHANGED <<op, payment>>

ApiSend ==
    /\ api.pc = "sending"
    /\ \/ /\ card
          /\ net' = net \cup {Msg("auth", op[api.key].acq, api.key, "none")}
          /\ api' = [api EXCEPT !.pc = "waiting"]
          /\ UNCHANGED <<payment, op, told>>
       \/ ApiNotSent
    /\ UNCHANGED <<book, card, worker, crashes>>

\* T2: the answer arrived; the merchant hears it only after it is recorded.
ApiReceiveAuth ==
    /\ api.pc \in {"waiting", "inquiring"}
    /\ \E m \in net :
         /\ m.kind = "authResp" /\ m.key = api.key
         /\ net' = net \ {m}
         /\ RecordFact(api.key, m.result)
    /\ told' = AnswerAfter(payment')
    /\ api' = [api EXCEPT !.pc = "done"]
    /\ UNCHANGED <<book, card, worker, crashes>>

ApiTimeout ==
    /\ api.pc = "waiting"
    /\ IF InquireOnTimeout
       THEN /\ net' = net \cup {Msg("inq", op[api.key].acq, api.key, "none")}
            /\ api' = [api EXCEPT !.pc = "inquiring"]
            /\ UNCHANGED <<payment, op, told>>
       ELSE ApiNotSent
    /\ UNCHANGED <<book, card, worker, crashes>>

ApiReceiveInquiry ==
    /\ api.pc = "inquiring"
    /\ \E m \in net :
         /\ m.kind = "inqResp" /\ m.key = api.key
         /\ LET k == api.key
                resend == m.result = "notfound" /\ card /\ op[k].status = "SENDING" /\ Holds(k, api.epoch)
            IN /\ net' = (net \ {m}) \cup (IF resend THEN {Msg("auth", op[k].acq, k, "none")} ELSE {})
               /\ CASE m.result \in {"approved", "declined"} ->
                         /\ RecordFact(k, m.result)
                         /\ told' = AnswerAfter(payment')
                         /\ api' = [api EXCEPT !.pc = "done"]
                    [] resend ->
                         \* it has not arrived: send it again, with the same key
                         /\ api' = [api EXCEPT !.pc = "waiting"]
                         /\ UNCHANGED <<op, payment, told>>
                    [] OTHER ->
                         \* leave it to recovery and answer what the database says
                         /\ told' = Answer
                         /\ api' = [api EXCEPT !.pc = "done"]
                         /\ UNCHANGED <<op, payment>>
    /\ UNCHANGED <<book, card, worker, crashes>>

\* The inquiry got no answer within the deadline: the operation is UNKNOWN,
\* the merchant gets 202, and recovery takes over when the lease expires.
ApiGiveUp ==
    /\ api.pc = "inquiring"
    /\ IF op[api.key].status = "SENDING" /\ Holds(api.key, api.epoch)
       THEN op' = [op EXCEPT ![api.key].status = "UNKNOWN"]
       ELSE UNCHANGED op
    /\ told' = Answer
    /\ api' = [api EXCEPT !.pc = "done"]
    /\ UNCHANGED <<payment, book, net, card, worker, crashes>>

ApiCrash ==
    /\ api.pc \in {"sending", "waiting", "inquiring"}
    /\ crashes < MaxCrashes
    /\ api' = [api EXCEPT !.pc = "crashed"]
    /\ crashes' = crashes + 1
    /\ UNCHANGED <<payment, op, book, net, card, worker, told>>

-----------------------------------------------------------------------------
(* Time *)

\* A lease expires, even if its holder is still working: it was only slow.
ExpireLease ==
    /\ \E k \in Keys :
         /\ op[k].status \in Unresolved \cup {"REVERSING"}
         /\ op[k].lease
         /\ op' = [op EXCEPT ![k].lease = FALSE]
    /\ UNCHANGED <<payment, book, net, card, api, worker, told, crashes>>

\* The vault deletes the CVV after 10 minutes.
CardExpires ==
    /\ payment /= "NONE"
    /\ card
    /\ card' = FALSE
    /\ UNCHANGED <<payment, op, book, net, api, worker, told, crashes>>

-----------------------------------------------------------------------------
(* The recovery worker *)

\* Claim an operation whose lease expired. Each claim starts a new epoch.
WorkerClaim ==
    /\ worker.pc = "idle"
    /\ \E k \in Keys :
         /\ op[k].status \in Unresolved \cup {"REVERSING"}
         /\ ~op[k].lease
         /\ op' = [op EXCEPT ![k].epoch = @ + 1, ![k].lease = TRUE]
         /\ IF op[k].status = "REVERSING"
            THEN /\ net' = net \cup {Msg("rev", op[k].acq, k, "none")}
                 /\ worker' = [pc |-> "reversing", key |-> k, epoch |-> op[k].epoch + 1]
            ELSE /\ net' = net \cup {Msg("inq", op[k].acq, k, "none")}
                 /\ worker' = [pc |-> "inquiring", key |-> k, epoch |-> op[k].epoch + 1]
    /\ UNCHANGED <<payment, book, card, api, told, crashes>>

\* Decide to reverse operation k. With write-ahead, the decision is recorded
\* first, so a late approval can no longer be recorded as the outcome.
StartReversal(k, consumed) ==
    /\ op' = IF WriteAheadReversal
             THEN [op EXCEPT ![k].status = "REVERSING"]
             ELSE op
    /\ net' = (net \ consumed) \cup {Msg("rev", op[k].acq, k, "none")}
    /\ worker' = [worker EXCEPT !.pc = "reversing"]
    /\ UNCHANGED payment

WorkerReceiveInquiry ==
    /\ worker.pc = "inquiring"
    /\ \E m \in net :
         /\ m.kind = "inqResp" /\ m.key = worker.key
         /\ LET k == worker.key
                mine == op[k].status \in Unresolved /\ Holds(k, worker.epoch)
            IN CASE m.result \in {"approved", "declined"} ->
                      /\ net' = net \ {m}
                      /\ RecordFact(k, m.result)
                      /\ worker' = [worker EXCEPT !.pc = "idle"]
                 [] m.result = "notfound" /\ mine /\ card ->
                      \* it has not arrived: send it again, with the same key
                      /\ net' = (net \ {m}) \cup {Msg("auth", op[k].acq, k, "none")}
                      /\ worker' = [worker EXCEPT !.pc = "waiting"]
                      /\ UNCHANGED <<op, payment>>
                 [] m.result = "notfound" /\ mine /\ ~card ->
                      \* nothing to resend, but the request may still be on its way
                      IF ReverseWhenCardGone
                      THEN StartReversal(k, {m})
                      ELSE \* the mistake: treat "not found" as "never arrived"
                           /\ op' = [op EXCEPT ![k].status = "NOT_SENT", ![k].lease = FALSE]
                           /\ payment' = "FAILED"
                           /\ net' = net \ {m}
                           /\ worker' = [worker EXCEPT !.pc = "idle"]
                 [] OTHER ->
                      /\ net' = net \ {m}
                      /\ worker' = [worker EXCEPT !.pc = "idle"]
                      /\ UNCHANGED <<op, payment>>
    /\ UNCHANGED <<book, card, api, told, crashes>>

WorkerReceiveAuth ==
    /\ worker.pc \in {"waiting", "inquiring"}
    /\ \E m \in net :
         /\ m.kind = "authResp" /\ m.key = worker.key
         /\ net' = net \ {m}
         /\ RecordFact(worker.key, m.result)
    /\ worker' = [worker EXCEPT !.pc = "idle"]
    /\ UNCHANGED <<book, card, api, told, crashes>>

\* The resent authorization got no answer: ask again.
WorkerWaitTimeout ==
    /\ worker.pc = "waiting"
    /\ net' = net \cup {Msg("inq", op[worker.key].acq, worker.key, "none")}
    /\ worker' = [worker EXCEPT !.pc = "inquiring"]
    /\ UNCHANGED <<payment, op, book, card, api, told, crashes>>

\* An inquiry got no answer: ask again, or reverse after MaxInquiries.
WorkerInquiryTimeout ==
    /\ worker.pc = "inquiring"
    /\ LET k == worker.key IN
       IF op[k].inquiries + 1 < MaxInquiries
       THEN /\ op' = [op EXCEPT ![k].inquiries = @ + 1]
            /\ net' = net \cup {Msg("inq", op[k].acq, k, "none")}
            /\ UNCHANGED <<payment, worker>>
       ELSE IF op[k].status \in Unresolved /\ Holds(k, worker.epoch)
            THEN StartReversal(k, {})
            ELSE /\ worker' = [worker EXCEPT !.pc = "idle"]
                 /\ UNCHANGED <<payment, op, net>>
    /\ UNCHANGED <<book, card, api, told, crashes>>

WorkerReversalTimeout ==
    /\ worker.pc = "reversing"
    /\ net' = net \cup {Msg("rev", op[worker.key].acq, worker.key, "none")}
    /\ UNCHANGED <<payment, op, book, card, api, worker, told, crashes>>

\* The reversal is confirmed: nothing is live for that key. The payment ends
\* FAILED, or fails over to the other acquirer, written before it is sent.
WorkerReceiveReversal ==
    /\ worker.pc = "reversing"
    /\ \E m \in net :
         /\ m.kind = "revResp" /\ m.key = worker.key
         /\ LET k == worker.key
                pending == IF WriteAheadReversal THEN {"REVERSING"} ELSE Unresolved
            IN IF op[k].status \in pending
               THEN \/ /\ op' = [op EXCEPT ![k].status = "REVERSED", ![k].lease = FALSE]
                       /\ payment' = "FAILED"
                       /\ net' = net \ {m}
                       /\ worker' = [worker EXCEPT !.pc = "idle"]
                    \/ /\ card
                       /\ k < MaxAttempts
                       /\ IndexAllowsAnother(k)
                       /\ op' = [op EXCEPT ![k].status = "REVERSED", ![k].lease = FALSE,
                                           ![k + 1] = NewOp(Other(op[k].acq))]
                       /\ net' = (net \ {m}) \cup {Msg("auth", Other(op[k].acq), k + 1, "none")}
                       /\ worker' = [pc |-> "waiting", key |-> k + 1, epoch |-> 1]
                       /\ UNCHANGED payment
               ELSE /\ net' = net \ {m}
                    /\ worker' = [worker EXCEPT !.pc = "idle"]
                    /\ UNCHANGED <<op, payment>>
    /\ UNCHANGED <<book, card, api, told, crashes>>

WorkerCrash ==
    /\ worker.pc \in {"inquiring", "waiting", "reversing"}
    /\ crashes < MaxCrashes
    /\ worker' = [worker EXCEPT !.pc = "idle"]
    /\ crashes' = crashes + 1
    /\ UNCHANGED <<payment, op, book, net, card, api, told>>

-----------------------------------------------------------------------------
(* The acquirers and the network *)

\* The issuer decides once per key; a repeated key gets the same answer. With
\* Duplicates, the network may deliver the same request again later.
AuthorizeMsg(m) ==
    /\ m \in net
    /\ m.kind = "auth"
    /\ \E r \in {"approved", "declined"}, keep \in IF Duplicates THEN BOOLEAN ELSE {FALSE} :
         LET a == m.acq
             k == m.key
             now == IF book[a][k] = "none" THEN r ELSE book[a][k]
         IN /\ book' = [book EXCEPT ![a][k] = now]
            /\ net' = (IF keep THEN net ELSE net \ {m})
                      \cup {Msg("authResp", a, k, IF now = "reversed" THEN "declined" ELSE now)}
    /\ UNCHANGED <<payment, op, card, api, worker, told, crashes>>

InquireMsg(m) ==
    /\ m \in net
    /\ m.kind = "inq"
    /\ LET s == book[m.acq][m.key] IN
       net' = (net \ {m}) \cup {Msg("inqResp", m.acq, m.key, IF s = "none" THEN "notfound" ELSE s)}
    /\ UNCHANGED <<payment, op, book, card, api, worker, told, crashes>>

\* A reversal undoes an approval. With tombstones, it also marks a key the
\* acquirer has not seen yet, so a late request with that key is declined.
ReverseMsg(m) ==
    /\ m \in net
    /\ m.kind = "rev"
    /\ LET s == book[m.acq][m.key] IN
       book' = [book EXCEPT ![m.acq][m.key] =
                  CASE s = "approved" -> "reversed"
                    [] s = "none" /\ Tombstones -> "reversed"
                    [] OTHER -> s]
    /\ net' = (net \ {m}) \cup {Msg("revResp", m.acq, m.key, "ok")}
    /\ UNCHANGED <<payment, op, card, api, worker, told, crashes>>

AcquirerAuthorize == \E m \in net : AuthorizeMsg(m)
AcquirerInquire == \E m \in net : InquireMsg(m)
AcquirerReverse == \E m \in net : ReverseMsg(m)

Lose ==
    /\ \E m \in net : net' = net \ {m}
    /\ UNCHANGED <<payment, op, book, card, api, worker, told, crashes>>

\* The merchant asks again, with GET or by retrying with the same key.
MerchantAsks ==
    /\ payment /= "NONE"
    /\ told' = Answer
    /\ UNCHANGED <<payment, op, book, net, card, api, worker, crashes>>

-----------------------------------------------------------------------------

Init ==
    /\ payment = "NONE"
    /\ op = [k \in Keys |-> [acq |-> CHOOSE a \in Acquirers : TRUE, status |-> "NONE",
                             epoch |-> 0, lease |-> FALSE, inquiries |-> 0]]
    /\ book = [a \in Acquirers |-> [k \in Keys |-> "none"]]
    /\ net = {}
    /\ card = TRUE
    /\ api = [pc |-> "start", key |-> 1, epoch |-> 0]
    /\ worker = [pc |-> "idle", key |-> 1, epoch |-> 0]
    /\ told = "nothing"
    /\ crashes = 0

Next ==
    \/ ApiStart \/ ApiSend \/ ApiReceiveAuth \/ ApiTimeout \/ ApiReceiveInquiry
    \/ ApiGiveUp \/ ApiCrash
    \/ ExpireLease \/ CardExpires
    \/ WorkerClaim \/ WorkerReceiveInquiry \/ WorkerReceiveAuth \/ WorkerWaitTimeout
    \/ WorkerInquiryTimeout \/ WorkerReversalTimeout \/ WorkerReceiveReversal \/ WorkerCrash
    \/ AcquirerAuthorize \/ AcquirerInquire \/ AcquirerReverse
    \/ Lose \/ MerchantAsks

\* Processes keep running and time passes. The network may lose any message,
\* but a message that keeps being sent is eventually delivered and answered.
\* Liveness is checked without Duplicates: a request redelivered forever would
\* let the acquirer actions look fair while the request that matters is always
\* lost, which says nothing about the protocol. Duplicates matter for safety,
\* where they are on.
Fairness ==
    /\ WF_vars(ApiStart) /\ WF_vars(ApiSend) /\ WF_vars(ApiTimeout) /\ WF_vars(ApiGiveUp)
    /\ WF_vars(ExpireLease) /\ WF_vars(WorkerClaim)
    /\ WF_vars(WorkerWaitTimeout) /\ WF_vars(WorkerInquiryTimeout) /\ WF_vars(WorkerReversalTimeout)
    /\ SF_vars(AcquirerAuthorize) /\ SF_vars(AcquirerInquire) /\ SF_vars(AcquirerReverse)
    /\ SF_vars(ApiReceiveAuth) /\ SF_vars(ApiReceiveInquiry)
    /\ SF_vars(WorkerReceiveAuth) /\ SF_vars(WorkerReceiveInquiry) /\ SF_vars(WorkerReceiveReversal)

Spec == Init /\ [][Next]_vars /\ Fairness

-----------------------------------------------------------------------------
(* What must hold *)

TypeOK ==
    /\ payment \in {"NONE", "PROCESSING"} \cup Final
    /\ op \in [Keys -> [acq : Acquirers, status : OpStatuses, epoch : Nat,
                        lease : BOOLEAN, inquiries : 0..MaxInquiries]]
    /\ book \in [Acquirers -> [Keys -> {"none", "approved", "declined", "reversed"}]]
    /\ net \subseteq Messages
    /\ card \in BOOLEAN
    /\ api.pc \in {"start", "sending", "waiting", "inquiring", "done", "crashed"}
    /\ worker.pc \in {"idle", "inquiring", "waiting", "reversing"}
    /\ told \in {"nothing", "PROCESSING"} \cup Final
    /\ crashes \in 0..MaxCrashes

\* The database never holds two operations that may be live: the unique index.
AtMostOneMaybeLive == Cardinality({k \in Keys : op[k].status \in MaybeLive}) <= 1

\* INV01: never two live authorizations for the payment, across all acquirers.
NoTwoLiveAuthorizations == Cardinality(Live) <= 1

\* The payment's final status matches what the acquirers did.
DatabaseTellsTheTruth ==
    /\ payment = "AUTHORIZED" => Cardinality(Live) = 1
    /\ payment \in {"DECLINED", "FAILED"} => Live = {}

\* INV03: whatever the merchant was told is true.
MerchantToldTheTruth ==
    /\ told = "AUTHORIZED" => Cardinality(Live) = 1
    /\ told \in {"DECLINED", "FAILED"} => Live = {}

\* INV02: if the acquirers eventually answer, the payment reaches a final state.
EventuallyFinal == <>(payment \in Final)

=============================================================================
