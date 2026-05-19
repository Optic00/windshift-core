package handlers

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"windshift/internal/database"
)

// Regression tests for docs/bughunt-2026-05-19-pass-2.md findings #2 and #3.
//
// F2: portal/form submissions must only persist custom fields that are
// configured on the request type. Arbitrary submitted field IDs (whether or
// not the field exists as a definition) must be dropped silently so the
// endpoint cannot be used as an oracle for which fields are configured.
//
// F3: required-field validation must treat empty arrays, empty objects, and
// whitespace-only strings as blank. Scalar `false` and `0` are NOT blank.

func seedRequestTypeWithCustomFields(t *testing.T, db database.Database, rtID int, customFieldIdentifiers []string, requiredIdentifiers map[string]bool) {
	t.Helper()
	var itemTypeID int
	if err := db.QueryRow(`SELECT id FROM item_types LIMIT 1`).Scan(&itemTypeID); err != nil {
		t.Fatalf("no seeded item_type: %v", err)
	}
	// request_types.channel_id is NOT NULL; seed one channel per request type.
	res, err := db.Exec(`INSERT INTO channels (name, type, direction, status) VALUES (?, 'portal', 'inbound', 'enabled')`, "ch-rt-"+strconv.Itoa(rtID))
	if err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	chID64, _ := res.LastInsertId()
	if _, err := db.Exec(`
		INSERT INTO request_types (id, channel_id, name, item_type_id, config, is_active)
		VALUES (?, ?, ?, ?, '{}', true)
	`, rtID, int(chID64), "RT-"+strconv.Itoa(rtID), itemTypeID); err != nil {
		t.Fatalf("seed request_type: %v", err)
	}
	display := 0
	for _, id := range customFieldIdentifiers {
		if _, err := db.Exec(`
			INSERT INTO request_type_fields (request_type_id, field_identifier, field_type, is_required, display_order)
			VALUES (?, ?, 'custom', ?, ?)
		`, rtID, id, requiredIdentifiers[id], display); err != nil {
			t.Fatalf("seed request_type_field %s: %v", id, err)
		}
		display++
	}
}

// F2: submitted custom field IDs that aren't configured on the request type
// must be dropped from the validation result, even when a custom field
// definition exists in the system. The dropped key must not reach
// storeCustomFieldValues.
func TestValidateAndSeparateFields_DropsUnconfiguredCustomFields(t *testing.T) {
	db := newNegativeTestDB(t)
	const rtID = 100
	seedRequestTypeWithCustomFields(t, db, rtID, []string{"42"}, map[string]bool{"42": false})

	rt := rtID
	customFields := map[string]interface{}{
		"42": "allowed-value",   // configured on the RT → kept
		"99": "tampered-value",  // NOT configured → must be dropped
		"7":  "another-hidden",  // NOT configured → must be dropped
	}

	res, err := validateAndSeparateFields(context.Background(), db, &rt, "Title", "Desc", customFields)
	if err != nil {
		t.Fatalf("validateAndSeparateFields: %v", err)
	}
	if got, want := res.customFieldValues["42"], "allowed-value"; got != want {
		t.Errorf("configured field 42 = %v, want %q", got, want)
	}
	if _, present := res.customFieldValues["99"]; present {
		t.Errorf("unconfigured field 99 must not appear in customFieldValues; got %v", res.customFieldValues["99"])
	}
	if _, present := res.customFieldValues["7"]; present {
		t.Errorf("unconfigured field 7 must not appear in customFieldValues; got %v", res.customFieldValues["7"])
	}
}

// F2 negative path: submission must succeed (no 400) when extra fields are
// included — silent drop only. A 400 would act as an oracle telling probers
// which custom field IDs the request type accepts.
func TestValidateAndSeparateFields_UnconfiguredFieldDoesNotError(t *testing.T) {
	db := newNegativeTestDB(t)
	const rtID = 101
	seedRequestTypeWithCustomFields(t, db, rtID, []string{"5"}, map[string]bool{"5": false})

	rt := rtID
	_, err := validateAndSeparateFields(context.Background(), db, &rt, "Title", "Desc", map[string]interface{}{
		"5":      "ok",
		"hidden": "tampered",
	})
	if err != nil {
		t.Fatalf("expected silent drop, got error: %v", err)
	}
}

// F3: required-field validation must reject blank values for arrays, objects,
// and whitespace strings — what JSON `[]`, `{}`, and `"   "` unmarshal to.
func TestValidateAndSeparateFields_RequiredRejectsBlankComposites(t *testing.T) {
	db := newNegativeTestDB(t)
	const rtID = 102
	seedRequestTypeWithCustomFields(t, db, rtID, []string{"multi"}, map[string]bool{"multi": true})
	rt := rtID

	cases := []struct {
		name  string
		value interface{}
	}{
		{"nil", nil},
		{"empty-string", ""},
		{"whitespace", "   \t\n"},
		{"empty-array", []interface{}{}},
		{"empty-object", map[string]interface{}{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateAndSeparateFields(context.Background(), db, &rt, "Title", "Desc", map[string]interface{}{"multi": tc.value})
			if err == nil {
				t.Fatalf("expected required-field error for blank %s value, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), "field multi is required") {
				t.Errorf("expected required error mentioning field; got %v", err)
			}
		})
	}
}

// F3 happy path: false, 0, and a populated array must satisfy the required
// gate — they are legitimate scalar/composite values, not blanks.
func TestValidateAndSeparateFields_RequiredAcceptsFalsyAndNonEmpty(t *testing.T) {
	db := newNegativeTestDB(t)
	const rtID = 103
	seedRequestTypeWithCustomFields(t, db, rtID, []string{"f"}, map[string]bool{"f": true})
	rt := rtID

	cases := []struct {
		name  string
		value interface{}
	}{
		{"false", false},
		{"zero-int", 0},
		{"zero-float", 0.0},
		{"populated-array", []interface{}{"a"}},
		{"populated-object", map[string]interface{}{"k": "v"}},
		{"non-empty-string", "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateAndSeparateFields(context.Background(), db, &rt, "Title", "Desc", map[string]interface{}{"f": tc.value})
			if err != nil {
				t.Errorf("expected required gate to accept %s value, got error: %v", tc.name, err)
			}
		})
	}
}
