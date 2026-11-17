package frontend

import (
	"encoding/json"
	"net/http"
)

// maxBodyBytes is the largest JSON body the frontend reads itself (the passkey ceremony).
const maxBodyBytes = 4 << 20

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil { // a value that cannot be encoded is an error, not an empty 200
		status, body = http.StatusInternalServerError, []byte(`{"error":"the answer could not be encoded"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(append(body, '\n'))
}
