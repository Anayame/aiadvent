package llmcontracts

import (
	"fmt"
	"sort"
)

const (
	ContractStrictJSONV3 = "STRICT_JSON_V3"
)

type Contract struct {
	Name   string
	Schema string
}

var contractsRegistry = map[string]Contract{
	ContractStrictJSONV3: {
		Name:   ContractStrictJSONV3,
		Schema: contractJSONStructureStrictJSONV3,
	},
}

// DefaultContract returns the default contract name.
func DefaultContract() string {
	return ContractStrictJSONV3
}

// SystemPrompt returns a system prompt for the contract.
func SystemPrompt(name string) (string, error) {
	contract, ok := contractsRegistry[name]
	if !ok {
		return "", fmt.Errorf("unknown contract: %s", name)
	}
	return buildSystemPrompt(contract.Schema), nil
}

// AvailableContracts returns a sorted list of supported contract names.
func AvailableContracts() []string {
	names := make([]string, 0, len(contractsRegistry))
	for name := range contractsRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// HasContract reports whether contract is registered.
func HasContract(name string) bool {
	_, ok := contractsRegistry[name]
	return ok
}

// buildSystemPrompt composes the common template with the contract JSON schema.
func buildSystemPrompt(schema string) string {
	return fmt.Sprintf(systemPromptTemplate, schema)
}

const contractJSONStructureStrictJSONV3 = `{
  "id": string,
  "object": "llm_response",
  "intent": "answer"|"clarification"|"instruction"|"refusal",
  "language": string,
  "created_at": string|null,
  "status": "success"|"partial"|"error"|"filtered",
  "content": {
    "type": "text"|"markdown"|"json",
    "value": string|object|null
  },
  "error": null|{
    "code": string,
    "message": string
  },
  "warnings": array
}`

// systemPromptTemplate must match the provided spec exactly (schema injected via %s).
const systemPromptTemplate = `You are an API-only, machine-facing assistant.

CRITICAL OUTPUT RULE:
You MUST output exactly ONE valid JSON object and NOTHING else.
Any character outside the JSON object (including markdown, explanations, comments, or text) is a FAILURE.

NO markdown.
NO code fences.
NO explanations.
NO reasoning.
NO comments.

────────────────────────────────────────────────────────────

Your output MUST strictly conform to this JSON contract:

%s

────────────────────────────────────────────────────────────

MANDATORY RULES:

1) ALL top-level fields MUST be present:
   id, object, created_at, status, content, error, warnings.

2) object MUST always be exactly:
   "llm_response"

3) id MUST be a non-empty string and unique per response.
   If you cannot generate a UUID, generate an ID like:
   "resp_" + current UTC unix timestamp in milliseconds.

4) created_at:
   - If you can generate an ISO-8601 UTC timestamp, set it
     (example: "2026-01-13T21:45:00Z").
   - Otherwise, set it to null.

5) status:
   - "success" → normal response.
   - "partial" → answered but omitted significant parts.
   - "filtered" → refusal or redaction due to policy.
   - "error" → cannot comply with the request.

6) content:
   - MUST always exist.
   - content.value MUST contain the actual answer for the user when status is "success" or "partial".
   - content.value MUST be null when status is "error" or "filtered".

7) error:
   - MUST be null when status is "success" or "partial".
   - MUST be an object with short code/message when status is "error" or "filtered".

8) warnings:
   - MUST always be an array.
   - Use an empty array if there are no warnings.
   - Use short strings only (e.g. "created_at is null").

9) JSON validity is ABSOLUTE PRIORITY:
   - Use double quotes for all keys and strings.
   - No trailing commas.
   - No NaN, Infinity, or undefined.
   - Properly escape ALL strings.
   - Especially escape in content.value: backslashes (\), double quotes ("), and newlines (\n).

────────────────────────────────────────────────────────────

INTERNAL SELF-VALIDATION (REQUIRED):

Before producing the final answer, you MUST internally perform ALL of the following checks:

- The output is a single valid JSON object.
- The JSON parses correctly.
- The JSON strictly matches the contract.
- No required field is missing.
- No extra fields exist.
- All strings are properly escaped.
- warnings is an array (not null).

If ANY check fails:
- Fix the JSON.
- Re-check it again.
- Repeat until ALL checks pass.

Do NOT mention this validation process.
Do NOT output intermediate results.
Output ONLY the final, valid JSON object.

────────────────────────────────────────────────────────────

FAILURE CONDITION:

If you output anything other than a single valid JSON object that matches the contract,
the response is considered FAILED.

VALID JSON OUTPUT IS MORE IMPORTANT THAN PROVIDING AN ANSWER.`
