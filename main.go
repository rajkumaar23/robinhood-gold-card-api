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
	"time"
)

var cfg struct {
	username         string
	password         string
	deviceToken      string
	clientID         string
	creditCustomerID string
	port             string
}

const baseURL = "https://api.robinhood.com/creditcard"

func main() {
	cfg.username = mustEnv("ROBINHOOD_USERNAME")
	cfg.password = mustEnv("ROBINHOOD_PASSWORD")
	cfg.deviceToken = mustEnv("ROBINHOOD_DEVICE_TOKEN")
	cfg.clientID = mustEnv("ROBINHOOD_CLIENT_ID")
	cfg.creditCustomerID = mustEnv("ROBINHOOD_CREDIT_CUSTOMER_ID")
	cfg.port = getEnv("PORT", "8080")

	http.HandleFunc("/balance", handleBalance)
	http.HandleFunc("/transactions", handleTransactions)

	log.Printf("robinhood-api listening on :%s", cfg.port)
	log.Fatal(http.ListenAndServe(":"+cfg.port, nil))
}

// --- handlers ---

func handleBalance(w http.ResponseWriter, r *http.Request) {
	token, err := login()
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
	if err := graphql(token, `
		query BalanceQuery($creditCustomerId: String!) {
			creditAccount(q: {creditCustomerId: $creditCustomerId}) {
				balances { currentMicro }
			}
		}`, "BalanceQuery", map[string]any{"creditCustomerId": cfg.creditCustomerID}, &result); err != nil {
		jsonError(w, "balance query failed: "+err.Error(), http.StatusBadGateway)
		return
	}

	jsonOK(w, map[string]any{
		"balance": result.Data.CreditAccount.Balances.CurrentMicro / 1_000_000,
	})
}

func handleTransactions(w http.ResponseWriter, r *http.Request) {
	token, err := login()
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
	if err := graphql(token, `
		query TransactionListQuery($q: TransactionSearchRequest!) {
			transactionSearch(q: $q) {
				items {
					amountMicro flow transactionStatus transactionAt visibility
					merchantDetails { rawMerchantName locality subdivision }
				}
			}
		}`, "TransactionListQuery", map[string]any{
		"q": map[string]any{
			"creditCustomerId": cfg.creditCustomerID,
			"filters":          map[string]any{"values": []string{}},
			"sortDetails":      map[string]any{"field": "TIME", "ascending": false},
			"limit":            50,
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
	}

	var txs []Transaction
	for _, item := range result.Data.TransactionSearch.Items {
		if item.Visibility != "VISIBLE" || item.TransactionStatus != "POSTED" {
			continue
		}

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
			Date:        time.UnixMilli(item.TransactionAt).Format("2006-01-02"),
			Description: desc,
			Amount:      item.AmountMicro / 1_000_000,
			Type:        txType,
		})
	}

	jsonOK(w, txs)
}

// --- Robinhood API helpers ---

func login() (string, error) {
	body, _ := json.Marshal(map[string]string{
		"grant_type":     "password",
		"username":       cfg.username,
		"password":       cfg.password,
		"scope":          "credit-card",
		"client_id":      cfg.clientID,
		"device_token":   cfg.deviceToken,
		"device_label":   "iPhone - iPhone 16",
		"challenge_type": "sms",
	})

	resp, err := http.Post(baseURL+"/auth/login", "application/json", bytes.NewReader(body))
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
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.AccessToken, nil
}

func graphql(token, query, operation string, variables map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{
		"query":         query,
		"operationName": operation,
		"variables":     variables,
	})

	req, _ := http.NewRequest("POST", baseURL+"/graphql", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-X1-Client", fmt.Sprintf("mobile-app-rh@1.84.0@%s", cfg.deviceToken))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

// --- helpers ---

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("required env var %s is not set", key)
	}
	return v
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
