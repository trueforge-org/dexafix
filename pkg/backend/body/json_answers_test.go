package bodybackend

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnAnswerThatCannotBeEncodedIsAnError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]float64{"v": math.NaN()})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status %d, body %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	writeJSON(rec, http.StatusNoContent, nil)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("no content: %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	writeJSON(rec, http.StatusAccepted, map[string]int{"a": 1})
	if rec.Code != http.StatusAccepted || rec.Body.String() != "{\"a\":1}\n" {
		t.Errorf("status %d, body %q", rec.Code, rec.Body.String())
	}
}
