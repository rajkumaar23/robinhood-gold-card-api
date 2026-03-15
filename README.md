# robinhood-gold-card-api

A minimal Go HTTP service that exposes the Robinhood Gold Card credit card API — balance and transactions — over two simple POST endpoints.

> The API endpoints, authentication flow, and request/response structure were discovered by reverse engineering the Robinhood iOS app using a MITM proxy.

## Run

```bash
go run main.go
# PORT=9000 go run main.go  # optional, defaults to 8080
```

## Endpoints

Both endpoints accept a JSON body. All fields in the **Credentials** section are required.

### Credentials (all endpoints)

| Field | Type | Description |
|---|---|---|
| `username` | string | Robinhood account email |
| `password` | string | Robinhood account password |
| `device_token` | string | Device token registered with Robinhood |
| `client_id` | string | OAuth client ID for the credit card app |
| `credit_customer_id` | string | Credit customer ID for your account |

---

### `POST /balance`

Returns the current statement balance.

**Response**
```json
{ "balance": 148.09 }
```

---

### `POST /transactions`

Returns transactions up to the requested limit. Filtering by `status`/`visibility` is left to the caller.

**Additional fields**

| Field | Type | Default | Description |
|---|---|---|---|
| `limit` | int | `50` | Number of transactions to fetch |
| `sort_field` | string | `"TIME"` | Field to sort by |
| `sort_ascending` | bool | `false` | Sort direction |

**Response**
```json
[
  {
    "date": "2026-03-13",
    "description": "ANTHROPIC ANTHROPIC.COM CA",
    "amount": 5.53,
    "type": "withdrawal",
    "status": "POSTED",
    "visibility": "VISIBLE"
  }
]
```

| Field | Values |
|---|---|
| `type` | `"withdrawal"` (purchase) · `"deposit"` (payment/refund) |
| `status` | `"POSTED"` · `"PENDING"` |
| `visibility` | `"VISIBLE"` · `"HIDDEN"` |

## License

[MIT](LICENSE)
