package jsonid

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
)

const opsID int64 = 1152921504606853875

func TestAJSONNumberLosesTheOpsIdThatAStringKeeps(t *testing.T) {
	asNumber, err := json.Marshal(opsID)
	if err != nil {
		t.Fatal(err)
	}
	var rounded float64
	if err := json.Unmarshal(asNumber, &rounded); err != nil {
		t.Fatal(err)
	}
	if int64(rounded) == opsID {
		t.Fatalf("the fixture id %d is still a JavaScript-safe JSON number", opsID)
	}

	asString, err := json.Marshal(strconv.FormatInt(opsID, 10))
	if err != nil {
		t.Fatal(err)
	}
	var kept string
	if err := json.Unmarshal(asString, &kept); err != nil {
		t.Fatal(err)
	}
	if kept != strconv.FormatInt(opsID, 10) {
		t.Fatalf("string id = %q, want the exact digits", kept)
	}
}

func TestParseAcceptsAStringOrANumberAndRejectsJunk(t *testing.T) {
	for _, test := range []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{name: "empty", input: "", want: 0},
		{name: "null", input: "null", want: 0},
		{name: "string", input: `"1152921504606853875"`, want: opsID},
		{name: "number", input: "1152921504606853875", want: opsID},
		{name: "small number", input: "42", want: 42},
		{name: "quoted small", input: `"42"`, want: 42},
		{name: "scientific", input: "1.152921504606854e+18", wantErr: true},
		{name: "object", input: `{"id":1}`, wantErr: true},
		{name: "blank string", input: `""`, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseJSON([]byte(test.input))
			if test.wantErr {
				if err == nil {
					t.Fatalf("ParseJSON(%s) = %d, want an error", test.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseJSON(%s): %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("ParseJSON(%s) = %d, want %d", test.input, got, test.want)
			}
		})
	}
}

func TestDecimalRoundTripKeepsOpsDigitsAndAcceptsANumber(t *testing.T) {
	var fromString Decimal
	if err := json.Unmarshal([]byte(`"1152921504606853875"`), &fromString); err != nil {
		t.Fatal(err)
	}
	if fromString.Int64() != opsID {
		t.Fatalf("string form = %d, want %d", fromString.Int64(), opsID)
	}
	var fromNumber Decimal
	if err := json.Unmarshal([]byte(`1152921504606853875`), &fromNumber); err != nil {
		t.Fatal(err)
	}
	if fromNumber.Int64() != opsID {
		t.Fatalf("number form = %d, want %d", fromNumber.Int64(), opsID)
	}
	encoded, err := json.Marshal(fromString)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `"1152921504606853875"` {
		t.Fatalf("marshal = %s, want a JSON string", encoded)
	}
}

func TestCellStringifiesIdentityAndUnsafeIntegers(t *testing.T) {
	for _, test := range []struct {
		column string
		value  any
		want   any
	}{
		{column: "id", value: int64(11), want: "11"},
		{column: "supersedes", value: opsID, want: "1152921504606853875"},
		{column: "canonical_id", value: int64(4), want: "4"},
		{column: "n", value: int64(3), want: int64(3)},
		{column: "n", value: opsID, want: "1152921504606853875"},
		{column: "count", value: int64(1908), want: int64(1908)},
		{column: "metadata", value: `{"supersedes": 1152921504606853875, "n": 2}`,
			want: `{"n":2,"supersedes":"1152921504606853875"}`},
	} {
		t.Run(fmt.Sprintf("%s=%v", test.column, test.value), func(t *testing.T) {
			got := Cell(test.column, test.value)
			if fmt.Sprint(got) != fmt.Sprint(test.want) {
				t.Fatalf("Cell(%q, %#v) = %#v, want %#v", test.column, test.value, got, test.want)
			}
		})
	}
}

func TestRewriteMapStringifiesNestedIds(t *testing.T) {
	got := RewriteMap(map[string]any{
		"n":          json.Number("7"),
		"id":         json.Number("1152921504606853875"),
		"nested":     map[string]any{"supersedes": int64(opsID), "ok": true},
		"unrelated":  opsID,
		"safe_count": int64(4),
	})
	if got["id"] != "1152921504606853875" {
		t.Fatalf("id = %#v, want a decimal string", got["id"])
	}
	nested, _ := got["nested"].(map[string]any)
	if nested["supersedes"] != "1152921504606853875" {
		t.Fatalf("nested supersedes = %#v", nested["supersedes"])
	}
	if got["unrelated"] != "1152921504606853875" {
		t.Fatalf("unsafe unrelated = %#v, want a decimal string", got["unrelated"])
	}
	if got["safe_count"] != int64(4) {
		t.Fatalf("safe_count = %#v, want int64(4)", got["safe_count"])
	}
	if fmt.Sprint(got["n"]) != "7" {
		t.Fatalf("n = %#v, want 7", got["n"])
	}
}

func TestIntVarParsesTheNumericFormAndTheExactDigits(t *testing.T) {
	var value IntVar
	if err := value.Set("1152921504606853875"); err != nil {
		t.Fatal(err)
	}
	if int64(value) != opsID {
		t.Fatalf("Set digits = %d, want %d", value, opsID)
	}
	if err := value.Set("42"); err != nil {
		t.Fatal(err)
	}
	if int64(value) != 42 {
		t.Fatalf("Set numeric form = %d, want 42", value)
	}
	if err := value.Set("1e18"); err == nil {
		t.Fatal("scientific notation was accepted")
	}
}

func TestIntsRoundTripAsStringsAndStillAcceptNumbers(t *testing.T) {
	encoded, err := json.Marshal(Ints{opsID, 4})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `["1152921504606853875","4"]` {
		t.Fatalf("marshal = %s", encoded)
	}
	var fromNumbers Ints
	if err := json.Unmarshal([]byte(`[1152921504606853875, 4]`), &fromNumbers); err != nil {
		t.Fatal(err)
	}
	if len(fromNumbers) != 2 || fromNumbers[0] != opsID || fromNumbers[1] != 4 {
		t.Fatalf("from numbers = %v", fromNumbers)
	}
}
