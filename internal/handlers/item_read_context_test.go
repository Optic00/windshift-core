package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestRespondItemReadErrorDoesNotWriteAfterClientCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("GET", "/api/items", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()

	new(ItemHandler).respondItemReadError(recorder, req, fmt.Errorf("list items: %w", context.Canceled))

	if recorder.Body.Len() != 0 {
		t.Fatalf("response body = %q, want no write after cancellation", recorder.Body.String())
	}
	if recorder.Code != 200 {
		t.Fatalf("response status = %d, want untouched recorder status 200", recorder.Code)
	}
}
