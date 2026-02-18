FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY function/go.mod ./
RUN go mod download

COPY function/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o function .

FROM alpine:latest

WORKDIR /app
COPY --from=builder /app/function .

EXPOSE 8080
ENTRYPOINT ["./function"]
