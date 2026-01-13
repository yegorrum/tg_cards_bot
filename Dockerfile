FROM golang:1.25.4-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git

COPY go.mod ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o tg_cards_bot ./cmd || true

FROM alpine:3.20

WORKDIR /app

COPY --from=builder /app/tg_cards_bot .
COPY --from=builder /app/migrations /app/migrations

RUN apk add --no-cache ca-certificates

EXPOSE 8080

CMD ["./app/tg_cards_bot"]