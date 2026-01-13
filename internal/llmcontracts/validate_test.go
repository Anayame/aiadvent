package llmcontracts

import (
	"testing"
)

func TestValidateValidSuccess(t *testing.T) {
	raw := `{
		"id": "resp_1",
		"object": "llm_response",
		"intent": "answer",
		"language": "ru",
		"created_at": "2026-01-13T21:45:00Z",
		"status": "success",
		"content": {
			"type": "text",
			"value": "hello"
		},
		"error": null,
		"warnings": []
	}`

	res, err := Validate(ContractStrictJSONV3, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsValid {
		t.Fatalf("expected valid result, got errors: %+v", res.Errors)
	}
}

func TestValidateRejectsExtraField(t *testing.T) {
	raw := `{
		"id": "resp_1",
		"object": "llm_response",
		"intent": "answer",
		"language": "ru",
		"created_at": null,
		"status": "success",
		"content": {
			"type": "text",
			"value": "ok"
		},
		"error": null,
		"warnings": [],
		"extra": 1
	}`

	res, err := Validate(ContractStrictJSONV3, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsValid {
		t.Fatalf("expected invalid because of extra field")
	}
}

func TestValidateRejectsInvalidObject(t *testing.T) {
	raw := `{
		"id": "resp_1",
		"object": "wrong",
		"intent": "answer",
		"language": "ru",
		"created_at": null,
		"status": "success",
		"content": {
			"type": "text",
			"value": "ok"
		},
		"error": null,
		"warnings": []
	}`

	res, err := Validate(ContractStrictJSONV3, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsValid {
		t.Fatalf("expected invalid because of object mismatch")
	}
}

func TestValidateRejectsErrorOnSuccess(t *testing.T) {
	raw := `{
		"id": "resp_1",
		"object": "llm_response",
		"intent": "answer",
		"language": "ru",
		"created_at": null,
		"status": "success",
		"content": {
			"type": "text",
			"value": "ok"
		},
		"error": {"code":"fail","message":"oops"},
		"warnings": []
	}`

	res, err := Validate(ContractStrictJSONV3, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsValid {
		t.Fatalf("expected invalid because error should be null")
	}
}

func TestValidateRejectsWarningsNull(t *testing.T) {
	raw := `{
		"id": "resp_1",
		"object": "llm_response",
		"intent": "answer",
		"language": "ru",
		"created_at": null,
		"status": "success",
		"content": {
			"type": "text",
			"value": "ok"
		},
		"error": null,
		"warnings": null
	}`

	res, err := Validate(ContractStrictJSONV3, raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsValid {
		t.Fatalf("expected invalid because warnings must be array")
	}
}
