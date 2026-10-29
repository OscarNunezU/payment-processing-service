# Functions for the scenarios that CI runs against the local environment
# (.github/workflows/ci.yml). They use the local keys from the README.

# tok NUMBER prints a token for the test card NUMBER.
tok() {
  curl --fail --silent --show-error localhost:8090/v1/tokens -H 'Authorization: Bearer pk_sim_local_only' \
    -d "{\"number\":\"$1\",\"exp_month\":12,\"exp_year\":2035,\"cvc\":\"123\"}" | jq -er .token
}

# pay ORDER NUMBER pays 15,000 CLP for ORDER with the card NUMBER, with ORDER
# as the idempotency key, and prints the HTTP status.
pay() {
  local token
  token=$(tok "$2") || return 1
  curl --silent --show-error --output /dev/null --write-out '%{http_code}' localhost:8080/v1/payments \
    -H 'Authorization: Bearer sk_test_local_tienda_uno' -H "Idempotency-Key: $1" \
    -d "{\"order_id\":\"$1\",\"amount\":15000,\"currency\":\"CLP\",\"card_token\":\"$token\"}"
}

# payment ORDER prints the payment of ORDER, with its history.
payment() {
  local id
  id=$(curl --fail --silent --show-error "localhost:8080/v1/payments?order_id=$1" \
    -H 'Authorization: Bearer sk_test_local_tienda_uno' | jq -er '.data[0].id') || return 1
  curl --fail --silent --show-error "localhost:8080/v1/payments/$id" -H 'Authorization: Bearer sk_test_local_tienda_uno'
}

# settle ORDER SECONDS waits up to SECONDS for the payment of ORDER to reach a
# final state, and prints it.
settle() {
  local p
  for _ in $(seq "$2"); do
    p=$(payment "$1") || return 1
    if [ "$(jq -r .status <<<"$p")" != PROCESSING ]; then
      echo "$p"
      return 0
    fi
    sleep 1
  done
  echo "the payment of order $1 is still PROCESSING after $2 s" >&2
  return 1
}

# book KEY prints what the acquirer's book holds for KEY.
book() {
  curl --fail --silent --show-error localhost:8091/admin/book | jq -c --arg key "$1" '[.data[] | select(.key == $key)]'
}
