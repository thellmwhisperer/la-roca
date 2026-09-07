package sqlgate

import "strings"

func RejectUnqualified(stmt string, schemas []Schema) error {
	if len(schemas) == 0 {
		return nil
	}
	g, err := openGate(true, schemas, true)
	if err != nil {
		return err
	}
	defer g.Close()
	_, err = g.Validate(stmt)
	return err
}

func (g *Gate) RejectUnqualified(stmt string) error {
	if g == nil {
		return nil
	}
	return RejectUnqualified(stmt, g.schemas)
}

func qualifiedCandidates(name string, schemas []Schema) []string {
	want := strings.ToLower(name)
	var candidates []string
	seen := map[string]bool{}
	for _, schema := range schemas {
		if schema.Name == "" || strings.EqualFold(schema.Name, "main") ||
			strings.EqualFold(schema.Name, "temp") {
			continue
		}
		for _, table := range schema.Tables {
			if IsHiddenTable(table.Name) || !strings.EqualFold(table.Name, want) {
				continue
			}
			candidate := schema.Name + "." + table.Name
			key := strings.ToLower(candidate)
			if seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}
