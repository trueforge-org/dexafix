package mainbackend

import (
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A value that cannot be encoded (a NaN) is an error, not a 200 with an empty body that a caller
// cannot tell from "nothing to say".
func TestAnswersThatCannotBeEncodedAreErrors(t *testing.T) {
	bad := map[string]float64{"height_cm": math.NaN()}
	for name, write := range map[string]func(http.ResponseWriter){
		"internal": func(w http.ResponseWriter) { internalJSON(w, http.StatusOK, bad) },
		"public":   func(w http.ResponseWriter) { writeJSON(w, http.StatusOK, bad) },
	} {
		rec := httptest.NewRecorder()
		write(rec)
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "could not be encoded") {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "NaN") {
			t.Errorf("%s: the value leaked into the answer", name)
		}
	}
	// What can be encoded still goes out as before, with a final newline.
	for name, write := range map[string]func(http.ResponseWriter){
		"internal": func(w http.ResponseWriter) { internalJSON(w, http.StatusCreated, map[string]int{"a": 1}) },
		"public":   func(w http.ResponseWriter) { writeJSON(w, http.StatusCreated, map[string]int{"a": 1}) },
	} {
		rec := httptest.NewRecorder()
		write(rec)
		if rec.Code != http.StatusCreated || rec.Body.String() != "{\"a\":1}\n" || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s: %d %q", name, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	internalJSON(rec, http.StatusNoContent, nil)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("no content: %d %q", rec.Code, rec.Body.String())
	}
}
