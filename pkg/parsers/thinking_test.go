package parsers

import "testing"

func TestPlaceThinkingKeepsTheExchangeWhenTheSessionGrows(t *testing.T) {
	exchanges := []Exchange{
		{Number: 1, Thinking: []Thinking{{Text: "same thought"}}},
	}
	PlaceThinking(exchanges)
	first := exchanges[0].Thinking[0].Position
	exchanges = append(exchanges, Exchange{Number: 2})
	PlaceThinking(exchanges)
	if exchanges[0].Thinking[0].Position != first {
		t.Fatalf("position moved from %v to %v when the session grew",
			first, exchanges[0].Thinking[0].Position)
	}
	if first != 1 {
		t.Fatalf("position = %v, want the exchange number", first)
	}
}
