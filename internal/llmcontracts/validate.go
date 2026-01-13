package llmcontracts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ContractError describes an error object inside the response.
type ContractError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ContractContent describes the content envelope.
type ContractContent struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

// LLMResponseContractV3 corresponds to STRICT_JSON_V3 payload.
type LLMResponseContractV3 struct {
	ID        string          `json:"id"`
	Object    string          `json:"object"`
	Intent    string          `json:"intent"`
	Language  string          `json:"language"`
	CreatedAt *string         `json:"created_at"`
	Status    string          `json:"status"`
	Content   ContractContent `json:"content"`
	Error     *ContractError  `json:"error"`
	Warnings  []string        `json:"warnings"`
}

// ValidationResult carries validation details.
type ValidationResult struct {
	IsValid       bool
	Errors        []string
	Parsed        *LLMResponseContractV3
	CanonicalJSON string
}

// Validate checks LLM response against registered contract.
func Validate(contractName string, llmText string) (ValidationResult, error) {
	result := ValidationResult{}

	if !HasContract(contractName) {
		return result, fmt.Errorf("unknown contract: %s", contractName)
	}

	raw := strings.TrimSpace(llmText)
	if raw == "" {
		result.Errors = append(result.Errors, "пустой ответ LLM")
		return result, nil
	}

	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()

	var resp LLMResponseContractV3
	if err := dec.Decode(&resp); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("ошибка JSON: %v", err))
		return result, nil
	}
	if err := ensureSingleJSON(dec); err != nil {
		result.Errors = append(result.Errors, err.Error())
	}

	result.Parsed = &resp
	result.Errors = append(result.Errors, validateContractV3(&resp)...)
	result.IsValid = len(result.Errors) == 0

	if result.IsValid {
		if canonical, err := json.Marshal(resp); err == nil {
			result.CanonicalJSON = string(canonical)
		}
	}

	return result, nil
}

func ensureSingleJSON(dec *json.Decoder) error {
	if dec.More() {
		return fmt.Errorf("в ответе найдено несколько JSON объектов")
	}

	var extra json.RawMessage
	if err := dec.Decode(&extra); err != nil && err != io.EOF {
		return fmt.Errorf("есть лишние данные после JSON: %v", err)
	}
	if len(bytes.TrimSpace(extra)) > 0 {
		return fmt.Errorf("есть лишние данные после JSON")
	}
	return nil
}

func validateContractV3(resp *LLMResponseContractV3) []string {
	var errs []string

	if resp.Object != "llm_response" {
		errs = append(errs, `object должен быть "llm_response"`)
	}
	if resp.ID == "" {
		errs = append(errs, "id не должен быть пустым")
	}
	switch resp.Intent {
	case "answer", "clarification", "instruction", "refusal":
	default:
		errs = append(errs, "некорректный intent")
	}
	if resp.Language == "" {
		errs = append(errs, "language не должен быть пустым")
	}
	if resp.Warnings == nil {
		errs = append(errs, "warnings должен быть массивом (может быть пустым)")
	}

	switch resp.Status {
	case "success", "partial", "error", "filtered":
	default:
		errs = append(errs, "некорректный status")
	}

	switch resp.Content.Type {
	case "text", "markdown", "json":
	default:
		errs = append(errs, "некорректный content.type")
	}

	valueBytes := bytes.TrimSpace(resp.Content.Value)
	valueIsNull := len(valueBytes) == 0 || bytes.Equal(valueBytes, []byte("null"))

	switch resp.Status {
	case "success", "partial":
		if resp.Error != nil {
			errs = append(errs, "error должен быть null при status success|partial")
		}
		if valueIsNull {
			errs = append(errs, "content.value не должен быть null при status success|partial")
		}
	case "error", "filtered":
		if resp.Error == nil {
			errs = append(errs, "error должен быть объектом при status error|filtered")
		}
		if !valueIsNull {
			errs = append(errs, "content.value должен быть null при status error|filtered")
		}
	}

	// Validate content.value type matches content.type.
	switch resp.Content.Type {
	case "json":
		if !valueIsNull {
			var obj interface{}
			if err := json.Unmarshal(resp.Content.Value, &obj); err != nil {
				errs = append(errs, "content.value должен быть JSON-объектом для type=json")
			} else {
				if _, ok := obj.(map[string]interface{}); !ok {
					errs = append(errs, "content.value должен быть JSON-объектом для type=json")
				}
			}
		}
	case "text", "markdown":
		if !valueIsNull {
			var s string
			if err := json.Unmarshal(resp.Content.Value, &s); err != nil {
				errs = append(errs, "content.value должен быть строкой для type="+resp.Content.Type)
			}
		}
	}

	if resp.Error != nil && (resp.Error.Code == "" || resp.Error.Message == "") {
		errs = append(errs, "error.code и error.message должны быть заполнены")
	}

	return errs
}
