# robinhood-gold-card-api

A minimal Go HTTP service that exposes the Robinhood Gold Card credit card API — balance and transactions — over two simple POST endpoints.

## Run

```bash
go run main.go
# PORT=9000 go run main.go  # optional, defaults to 8080
```

## Endpoints

### `POST /balance`

Returns the current statement balance.

**Request body**
```json
{
  "username": "you@example.com",
  "password": "...",
  "device_token": "...",
  "client_id": "...",
  "credit_customer_id": "..."
}
```

**Response**
```json
{
  "balance": 123.45
}
```

---

### `POST /transactions`

Returns posted transactions.

**Request body**
```json
{
  "username": "you@example.com",
  "password": "...",
  "device_token": "...",
  "client_id": "...",
  "credit_customer_id": "...",
  "limit": 50,
  "sort_field": "TIME",
  "sort_ascending": false
}
```

`limit`, `sort_field`, and `sort_ascending` are optional and default to `50`, `"TIME"`, and `false` respectively.

**Response**
```json
[
  {
    "date": "2026-03-14",
    "description": "WHOLE FOODS MARKET Seattle WA",
    "amount": 42.17,
    "type": "withdrawal"
  }
]
```

`type` is either `"withdrawal"` (purchase) or `"deposit"` (payment/refund).

## License

[MIT](LICENSE)
