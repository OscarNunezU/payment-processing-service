FROM golang:1.26.9-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

FROM gcr.io/distroless/static-debian13:nonroot AS acquirer-mock
COPY --from=build /out/acquirer-mock /acquirer-mock
EXPOSE 8081
ENTRYPOINT ["/acquirer-mock"]

# The last stage is the default target: building without --target gives the API.
FROM gcr.io/distroless/static-debian13:nonroot AS server
COPY --from=build /out/server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
