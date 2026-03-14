FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o robinhood-api .

FROM scratch
COPY --from=builder /app/robinhood-api /robinhood-api
EXPOSE 8080
ENTRYPOINT ["/robinhood-api"]
