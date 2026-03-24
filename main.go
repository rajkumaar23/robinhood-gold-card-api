package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // embed timezone database for scratch containers

	"github.com/golang-jwt/jwt/v5"
)

const baseURL = "https://api.robinhood.com/creditcard"

var tokenCache = struct {
	sync.Mutex
	tokens map[string]cachedToken
}{tokens: make(map[string]cachedToken)}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

func getCachedToken(username string) (string, bool) {
	tokenCache.Lock()
	defer tokenCache.Unlock()
	if ct, ok := tokenCache.tokens[username]; ok && time.Now().Before(ct.expiresAt) {
		return ct.token, true
	}
	return "", false
}

func setCachedToken(username, token string, expiresAt time.Time) {
	tokenCache.Lock()
	defer tokenCache.Unlock()
	tokenCache.tokens[username] = cachedToken{
		token:     token,
		expiresAt: expiresAt,
	}
}

func invalidateCachedToken(username string) {
	tokenCache.Lock()
	defer tokenCache.Unlock()
	delete(tokenCache.tokens, username)
}

type Credentials struct {
	Username         string `json:"username"`
	Password         string `json:"password"`
	DeviceToken      string `json:"device_token"`
	ClientID         string `json:"client_id"`
	CreditCustomerID string `json:"credit_customer_id"`
}

type TransactionRequest struct {
	Credentials
	Limit         int    `json:"limit"`
	SortField     string `json:"sort_field"`
	SortAscending bool   `json:"sort_ascending"`
}

func (t *TransactionRequest) applyDefaults() {
	if t.Limit == 0 {
		t.Limit = 50
	}
	if t.SortField == "" {
		t.SortField = "TIME"
	}
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/balance", corsMiddleware(handleBalance))
	http.HandleFunc("/transactions", corsMiddleware(handleTransactions))

	log.Printf("robinhood-api listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// --- handlers ---

func handleBalance(w http.ResponseWriter, r *http.Request) {
	var creds Credentials
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		jsonError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := creds.validate(); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}

	token, err := getToken(creds)
	if err != nil {
		jsonError(w, "login failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	type balanceResponse struct {
		Data struct {
			CreditAccount struct {
				Balances struct {
					CurrentMicro float64 `json:"currentMicro"`
				} `json:"balances"`
			} `json:"creditAccount"`
		} `json:"data"`
	}

	var result balanceResponse
	if err := graphql(creds, token, `
		query BalanceQuery($creditCustomerId: String!) {
			creditAccount(q: {creditCustomerId: $creditCustomerId}) {
				balances { currentMicro }
			}
		}`, "BalanceQuery", map[string]any{"creditCustomerId": creds.CreditCustomerID}, &result); err != nil {
		jsonError(w, "balance query failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	jsonOK(w, map[string]any{
		"balance": result.Data.CreditAccount.Balances.CurrentMicro / 1_000_000,
	})
}

func handleTransactions(w http.ResponseWriter, r *http.Request) {
	var req TransactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := req.Credentials.validate(); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	req.applyDefaults()

	creds := req.Credentials
	token, err := getToken(creds)
	if err != nil {
		jsonError(w, "login failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	type txResponse struct {
		Data struct {
			TransactionSearch struct {
				Items []struct {
					AmountMicro       float64 `json:"amountMicro"`
					Flow              string  `json:"flow"`
					Visibility        string  `json:"visibility"`
					TransactionStatus string  `json:"transactionStatus"`
					TransactionAt     int64   `json:"transactionAt"`
					MerchantDetails   struct {
						RawMerchantName string `json:"rawMerchantName"`
						Locality        string `json:"locality"`
						Subdivision     string `json:"subdivision"`
					} `json:"merchantDetails"`
				} `json:"items"`
			} `json:"transactionSearch"`
		} `json:"data"`
	}

	var result txResponse
	if err := graphql(creds, token, `
		query TransactionListQuery($q: TransactionSearchRequest!) {
			transactionSearch(q: $q) {
				items {
					amountMicro flow transactionStatus transactionAt visibility
					merchantDetails { rawMerchantName locality subdivision }
				}
			}
		}`, "TransactionListQuery", map[string]any{
		"q": map[string]any{
			"creditCustomerId": creds.CreditCustomerID,
			"filters":          map[string]any{"values": []string{}},
			"sortDetails":      map[string]any{"field": req.SortField, "ascending": req.SortAscending},
			"limit":            req.Limit,
		},
	}, &result); err != nil {
		jsonError(w, "transactions query failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	type Transaction struct {
		Date        string  `json:"date"`
		Description string  `json:"description"`
		Amount      float64 `json:"amount"`
		Type        string  `json:"type"`
		Status      string  `json:"status"`
		Visibility  string  `json:"visibility"`
	}

	txs := make([]Transaction, 0)
	for _, item := range result.Data.TransactionSearch.Items {
		desc := strings.TrimSpace(strings.Join([]string{
			item.MerchantDetails.RawMerchantName,
			item.MerchantDetails.Locality,
			item.MerchantDetails.Subdivision,
		}, " "))

		txType := "deposit"
		if item.Flow == "OUTBOUND" {
			txType = "withdrawal"
		}

		txs = append(txs, Transaction{
			Date:        time.UnixMilli(item.TransactionAt).Local().Format("2006-01-02"),
			Description: desc,
			Amount:      item.AmountMicro / 1_000_000,
			Type:        txType,
			Status:      item.TransactionStatus,
			Visibility:  item.Visibility,
		})
	}

	jsonOK(w, txs)
}

// --- Robinhood API helpers ---

func getToken(creds Credentials) (string, error) {
	if token, ok := getCachedToken(creds.Username); ok {
		return token, nil
	}
	return login(creds)
}

// jwtExpiry extracts the exp claim from a JWT without verifying the signature.
// Falls back to the provided fallback duration if parsing fails.
func jwtExpiry(tokenStr string, fallback time.Duration) time.Time {
	p := jwt.NewParser()
	claims := jwt.MapClaims{}
	// ParseUnverified skips signature verification — we only need the exp claim.
	if _, _, err := p.ParseUnverified(tokenStr, claims); err == nil {
		if exp, err := claims.GetExpirationTime(); err == nil && exp != nil {
			return exp.Time
		}
	}
	return time.Now().Add(fallback)
}

func login(creds Credentials) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"grant_type":     "password",
		"username":       creds.Username,
		"password":       creds.Password,
		"scope":          "credit-card",
		"client_id":      creds.ClientID,
		"device_token":   creds.DeviceToken,
		"device_label":   "iPhone - iPhone 16",
		"challenge_type": "sms",
	})

	loginReq, _ := http.NewRequest("POST", baseURL+"/auth/login", bytes.NewReader(body))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.Header.Set("User-Agent", "Robinhood Credit Card/1.84.0 (iOS 26.0.1;)")
	loginReq.Header.Set("X-X1-Client", fmt.Sprintf("mobile-app-rh@1.84.0@%s", creds.DeviceToken))

	resp, err := http.DefaultClient.Do(loginReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}

	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	expiresAt := jwtExpiry(result.AccessToken, time.Duration(result.ExpiresIn)*time.Second)
	setCachedToken(creds.Username, result.AccessToken, expiresAt)
	return result.AccessToken, nil
}

func graphql(creds Credentials, token, query, operation string, variables map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{
		"query":         query,
		"operationName": operation,
		"variables":     variables,
	})

	req, _ := http.NewRequest("POST", baseURL+"/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "Robinhood Credit Card/1.84.0 (iOS 26.0.1;)")
	req.Header.Set("X-X1-Client", fmt.Sprintf("mobile-app-rh@1.84.0@%s", creds.DeviceToken))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		invalidateCachedToken(creds.Username)
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

func (c Credentials) validate() error {
	switch {
	case c.Username == "":
		return fmt.Errorf("missing username")
	case c.Password == "":
		return fmt.Errorf("missing password")
	case c.DeviceToken == "":
		return fmt.Errorf("missing device_token")
	case c.ClientID == "":
		return fmt.Errorf("missing client_id")
	case c.CreditCustomerID == "":
		return fmt.Errorf("missing credit_customer_id")
	}
	return nil
}

// --- helpers ---

func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
