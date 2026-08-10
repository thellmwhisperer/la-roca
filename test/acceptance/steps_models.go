//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Steps for the model adapters. Still black box: not one symbol of the product
// is imported here.
//
// The providers are faked with real HTTP servers and the binary is pointed at
// them through its own configuration, which is the operator's surface and not a
// hook of the test's. That is what makes the scenario measure what the operator
// would get.
//
// A rule that governs this whole file: a scenario that does not declare a
// provider world runs with the model TURNED OFF. The machine running this suite
// may well have a real Ollama listening on its usual port, and a suite that
// contacted it would measure that machine instead of this binary.

// theCredential is the fake frontier credential. Its value is what F07-08 hunts
// for in every output, so it has to be recognizable and unlike anything else.
const theCredential = "sk-roca-acceptance-CREDENTIAL-VALUE-7741"

// theFrontierName is the frontier provider of these scenarios. A preset by key
// is used and not the subscription one, because the subscription one is a
// vendor's login and the real one belongs to the final battery with the
// operator's credential.
const theFrontierName = "deepseek"

// modelWorld is the scenario's provider world.
type modelWorld struct {
	// order is the provider order this scenario wants. Empty means the model is
	// off.
	order []string
	// fromTheFile says the order travels in the configuration file and not in
	// the environment. It is what the scenarios about configuration measure,
	// and it is why they are the only ones that do not set the variable.
	fromTheFile bool

	frontier      *httptest.Server
	frontierURL   string
	frontierKey   string
	frontierModel string

	local    *httptest.Server
	localURL string

	requests *requestLog
}

// requestLog counts what each fake provider received. It is what answers "the
// local provider has received no request".
type requestLog struct {
	mu sync.Mutex
	to map[string]int
}

func newRequestLog() *requestLog { return &requestLog{to: map[string]int{}} }

func (l *requestLog) note(who string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.to[who]++
}

func (l *requestLog) count(who string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.to[who]
}

// theGeneratedSQL is what the fake models answer. It is a legitimate SELECT
// over the seeded world, so a scenario that gets here really goes through the
// gate and really queries the database.
const theGeneratedSQL = "SELECT content FROM memories WHERE supersedes IS NULL LIMIT 5"

// modelEnvironment is what the binary is told about providers.
//
// The order goes in the environment and not in the file for one reason: the
// environment is resolved before the file and it therefore also covers the
// scenarios that write no configuration, which is most of them. What the
// scenarios about configuration declare does go into the file, and those clear
// this variable so the file is what decides.
func (m *world) modelEnvironment() []string {
	if m.models.orderInTheFile() {
		return nil
	}
	if len(m.models.order) == 0 {
		return []string{"ROCA_MODELS_ORDER=none"}
	}
	return []string{"ROCA_MODELS_ORDER=" + strings.Join(m.models.order, ",")}
}

// orderInTheFile says the scenario wrote the order into the configuration, so
// the environment must not override it.
func (w modelWorld) orderInTheFile() bool { return w.fromTheFile }

func (m *world) closeModels() {
	if m.models.frontier != nil {
		m.models.frontier.Close()
	}
	if m.models.local != nil {
		m.models.local.Close()
	}
	m.models = modelWorld{}
}

// --- worlds ---

// aValidFrontierCredential starts the fake frontier and writes its credential
// where an operator would write it.
func (m *world) aValidFrontierCredential() error {
	m.ensureRequestLog()
	m.models.frontierKey = theCredential
	return m.writeModelConfig()
}

func (m *world) theFrontierIsAvailable() error {
	m.ensureRequestLog()
	log := m.models.requests
	m.models.frontier = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.note("frontier")
		if strings.HasSuffix(r.URL.Path, "/models") {
			json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": theGeneratedSQL},
			}},
		})
	}))
	m.models.frontierURL = m.models.frontier.URL
	m.models.order = withProvider(m.models.order, theFrontierName)
	return m.writeModelConfig()
}

// thereIsNoNetwork leaves the frontier pointing where nobody answers. From the
// binary's side that is exactly what no network is: the vendor does not answer.
// The local floor keeps answering, because it is local.
func (m *world) thereIsNoNetwork() error {
	m.ensureRequestLog()
	if m.models.frontier != nil {
		m.models.frontier.Close()
		m.models.frontier = nil
	}
	m.models.frontierURL = deadEndpoint
	m.models.order = withProvider(m.models.order, theFrontierName)
	return m.writeModelConfig()
}

func (m *world) thereIsNoFrontierCredential() error {
	m.ensureRequestLog()
	m.models.frontierKey = ""
	if m.models.frontierURL == "" {
		m.models.frontierURL = deadEndpoint
	}
	m.models.order = withProvider(m.models.order, theFrontierName)
	return m.writeModelConfig()
}

func (m *world) theLocalModelIsAvailable() error {
	m.ensureRequestLog()
	log := m.models.requests
	m.models.local = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.note("local")
		switch r.URL.Path {
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{
				"models": []any{map[string]any{"name": theLocalModel, "model": theLocalModel}},
			})
		case "/api/chat":
			json.NewEncoder(w).Encode(map[string]any{
				"message": map[string]any{"role": "assistant", "content": theGeneratedSQL},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	m.models.localURL = m.models.local.URL
	m.models.order = withProvider(m.models.order, "ollama")
	return m.writeModelConfig()
}

func (m *world) theLocalModelIsNotAvailable() error {
	m.ensureRequestLog()
	if m.models.local != nil {
		m.models.local.Close()
		m.models.local = nil
	}
	m.models.localURL = deadEndpoint
	m.models.order = withProvider(m.models.order, "ollama")
	return m.writeModelConfig()
}

// theConfigurationDeclaresTheOrder writes the order into the file, which is
// what F07-06 is about: the order is decided by the configuration and not by
// the code.
func (m *world) theConfigurationDeclaresTheOrder() error {
	m.ensureRequestLog()
	m.models.order = []string{theFrontierName, "ollama"}
	m.models.fromTheFile = true
	if m.models.frontierURL == "" {
		m.models.frontierURL = deadEndpoint
	}
	if m.models.localURL == "" {
		m.models.localURL = deadEndpoint
	}
	return m.writeModelConfig()
}

// theConfigurationDeclaresAnUnknownProvider is F07-07: data the operator
// persisted that this build does not understand.
func (m *world) theConfigurationDeclaresAnUnknownProvider() error {
	m.ensureRequestLog()
	m.models.order = []string{theUnknownProvider, "ollama"}
	m.models.fromTheFile = true
	m.models.localURL = deadEndpoint
	return m.writeModelConfig()
}

const (
	// deadEndpoint is a port nobody listens on: the shape of "it does not
	// answer" without depending on the network of the machine running this.
	deadEndpoint       = "http://127.0.0.1:1"
	theLocalModel      = "qwen3.5:4b"
	theUnknownProvider = "telepathy"
)

func (m *world) ensureRequestLog() {
	if m.models.requests == nil {
		m.models.requests = newRequestLog()
	}
}

func withProvider(order []string, name string) []string {
	for _, already := range order {
		if already == name {
			return order
		}
	}
	return append(order, name)
}

// writeModelConfig writes the [models] section the way an operator would.
func (m *world) writeModelConfig() error {
	dir := filepath.Join(m.home, ".roca")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	var body strings.Builder
	body.WriteString("[models]\n")
	if m.models.fromTheFile {
		body.WriteString("order = [" + quotedList(m.models.order) + "]\n")
	}
	// Short budgets: these scenarios measure the decision, not the patience.
	body.WriteString("timeout_ms = 5000\nprobe_ms = 1000\n")

	if m.models.frontierURL != "" || m.models.frontierKey != "" {
		body.WriteString("\n[models." + theFrontierName + "]\n")
		body.WriteString("base_url = " + tomlString(orDeadEndpoint(m.models.frontierURL)) + "\n")
		model := m.models.frontierModel
		if model == "" {
			model = "the-frontier-model"
		}
		body.WriteString("model = " + tomlString(model) + "\n")
		if m.models.frontierKey != "" {
			body.WriteString("api_key = " + tomlString(m.models.frontierKey) + "\n")
		}
	}
	if m.models.localURL != "" {
		body.WriteString("\n[models.ollama]\n")
		body.WriteString("base_url = " + tomlString(m.models.localURL) + "\n")
		body.WriteString("model = " + tomlString(theLocalModel) + "\n")
	}
	return os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body.String()), 0o600)
}

func (m *world) configurationChoosesFrontierModel(model string) error {
	m.models.frontierModel = model
	m.models.order = withProvider(m.models.order, theFrontierName)
	if m.models.frontierURL == "" {
		m.models.frontierURL = deadEndpoint
	}
	return m.writeModelConfig()
}

func (m *world) loginWithModel(name, model string) error {
	command := exec.Command(m.binaryPath(), "login", name, "--model", model)
	command.Stdin = strings.NewReader(theCredential + "\n")
	return m.record("roca login "+name+" --model "+model, command)
}

func (m *world) configurationChoosesProviderModel(model, name string) error {
	raw, err := os.ReadFile(filepath.Join(m.home, ".roca", "config.toml"))
	if err != nil {
		return err
	}
	for _, want := range []string{"[models." + name + "]", "model = " + tomlString(model)} {
		if !strings.Contains(string(raw), want) {
			return fmt.Errorf("configuration does not carry %q:\n%s", want, raw)
		}
	}
	return nil
}

func (m *world) modelNarrationNames(model, name string) error {
	all := m.last.stdout + m.last.stderr
	path := filepath.Join(m.home, ".roca", "config.toml")
	for _, want := range []string{
		model, "from " + path, "models." + name + ".model",
		"roca login " + name + " --model <id>",
	} {
		if !strings.Contains(all, want) {
			return fmt.Errorf("model narration does not carry %q:\n%s", want, all)
		}
	}
	return nil
}

func orDeadEndpoint(url string) string {
	if url == "" {
		return deadEndpoint
	}
	return url
}

func tomlString(value string) string { return "\"" + value + "\"" }

func quotedList(values []string) string {
	quotedValues := make([]string, 0, len(values))
	for _, value := range values {
		quotedValues = append(quotedValues, tomlString(value))
	}
	return strings.Join(quotedValues, ", ")
}

// --- execution ---

// theGoldenBenchOfTheFastRoute writes a bench with only fast-route questions
// and runs it. It is what F07-05 needs: proving that with no model at all the
// deterministic route still works end to end.
func (m *world) theGoldenBenchOfTheFastRoute() error {
	path := filepath.Join(m.home, "fast-route.yaml")
	bench := `version: 1
generator: acceptance-suite
cases:
  - id: count-memories-es
    question: cuantas memorias hay
    expect_path: compiler
    expect_template: count_memories
  - id: count-memories-en
    question: how many memories are there
    expect_path: compiler
    expect_template: count_memories
  - id: search-seeded-term
    question: guiones largos
    expect_path: compiler
    expect_min_rows: 1
`
	if err := os.WriteFile(path, []byte(bench), 0o600); err != nil {
		return err
	}
	_, err := m.runWith("roca bench golden "+path,
		[]string{"bench", "golden", path, "--json", "--method", "like"})
	return err
}

// --- assertions ---

func (m *world) jsonKeyEqualToTheFrontier(key string) error {
	return m.jsonKeyEqualTo(key, theFrontierName)
}

func (m *world) theLocalProviderReceivedNoRequest() error {
	if m.models.requests == nil {
		return nil
	}
	if count := m.models.requests.count("local"); count != 0 {
		return fmt.Errorf("the local provider received %d requests and had to receive none", count)
	}
	return nil
}

func (m *world) noProviderWasContacted() error {
	if m.models.requests == nil {
		return nil
	}
	for _, who := range []string{"frontier", "local"} {
		if count := m.models.requests.count(who); count != 0 {
			return fmt.Errorf("%s was contacted %d times and the fast route needs no model",
				who, count)
		}
	}
	return nil
}

func (m *world) itDeclaresItDegradedToTheLocalFloor() error {
	if strings.Contains(strings.ToLower(m.last.stdout+m.last.stderr), "local floor") {
		return nil
	}
	return fmt.Errorf("it does not declare it fell to the local floor: %s", m.last.stdout)
}

// noActionAskedOfTheOperator is the other half of F07-02: falling to the floor
// is automatic, so nothing may be asked of whoever ran the query.
func (m *world) noActionAskedOfTheOperator() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	if _, degraded := document["degraded"]; degraded {
		return fmt.Errorf("it answered from the floor and still declares the answer degraded: %s",
			m.last.stdout)
	}
	if m.last.code != 0 {
		return fmt.Errorf("it answered and exited with %d", m.last.code)
	}
	return nil
}

func (m *world) itNamesEveryProviderTriedAndWhy() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	providers, ok := document["providers"].([]any)
	if !ok || len(providers) == 0 {
		return fmt.Errorf("it names no provider tried: %s", m.last.stdout)
	}
	for _, raw := range providers {
		attempt, _ := raw.(map[string]any)
		if name, _ := attempt["provider"].(string); name == "" {
			return fmt.Errorf("an attempt with no name: %v", attempt)
		}
		if reason, _ := attempt["reason"].(string); reason == "" {
			return fmt.Errorf("the provider %v does not say why it did not serve", attempt["provider"])
		}
	}
	return nil
}

func (m *world) itNamesTheCommandThatStartsTheLocalModel() error {
	everything := m.last.stdout + m.last.stderr
	if strings.Contains(everything, "ollama serve") || strings.Contains(everything, "ollama pull") {
		return nil
	}
	return fmt.Errorf("it does not name the exact command to install or start the local model: %s",
		everything)
}

func (m *world) theyAllExitWithCode(expected int) error {
	if m.last.code != expected {
		return fmt.Errorf("the bench exited with %d, want %d (%s)",
			m.last.code, expected, m.last.stderr)
	}
	return nil
}

// theyAllLeaveByThePath reads the bench's report and checks every case took the
// declared path. The bench already judges `expect_path`, so a green bench is
// the check; this reads the score to make the failure say which case failed.
func (m *world) theyAllLeaveByThePath(path string) error {
	document, err := m.json()
	if err != nil {
		return err
	}
	scores, ok := document["scores"].([]any)
	if !ok || len(scores) == 0 {
		return fmt.Errorf("the bench published no score: %s", m.last.stdout)
	}
	for _, raw := range scores {
		score, _ := raw.(map[string]any)
		passed, _ := score["passed"].(float64)
		total, _ := score["total"].(float64)
		if passed != total {
			return fmt.Errorf("%v of %v cases met their criterion (expected path %s): %s",
				passed, total, path, m.last.stdout)
		}
	}
	return nil
}

func (m *world) theProvidersAreListedInTheDeclaredOrder() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	providers, ok := document["providers"].([]any)
	if !ok {
		return fmt.Errorf("doctor lists no provider: %s", m.last.stdout)
	}
	var listed []string
	for _, raw := range providers {
		entry, _ := raw.(map[string]any)
		name, _ := entry["provider"].(string)
		listed = append(listed, name)
	}
	if strings.Join(listed, ",") != strings.Join(m.models.order, ",") {
		return fmt.Errorf("declared order %v, reported %v", m.models.order, listed)
	}
	return nil
}

func (m *world) eachOneDeclaresWhetherItIsAvailableAndWhy() error {
	document, err := m.json()
	if err != nil {
		return err
	}
	providers, _ := document["providers"].([]any)
	for _, raw := range providers {
		entry, _ := raw.(map[string]any)
		ready, isDeclared := entry["ready"].(bool)
		if !isDeclared {
			return fmt.Errorf("the provider %v does not declare whether it is available", entry["provider"])
		}
		if ready {
			continue
		}
		if reason, _ := entry["reason"].(string); reason == "" {
			return fmt.Errorf("the provider %v is not available and does not say why", entry["provider"])
		}
		if action, _ := entry["action"].(string); action == "" {
			return fmt.Errorf("the provider %v names no remedy", entry["provider"])
		}
	}
	return nil
}

func (m *world) aWarningNamesTheUnknownProvider() error {
	everything := m.last.stdout + m.last.stderr
	if !strings.Contains(everything, theUnknownProvider) {
		return fmt.Errorf("no warning names %q: %s", theUnknownProvider, everything)
	}
	return nil
}

func (m *world) thatWarningListsTheAvailableProviders() error {
	everything := m.last.stdout + m.last.stderr
	for _, expected := range []string{"ollama", "codex"} {
		if !strings.Contains(everything, expected) {
			return fmt.Errorf("the warning does not list %q among the available ones: %s",
				expected, everything)
		}
	}
	return nil
}

// noOutputCarriesTheCredential is F07-08 over every command of the scenario.
func (m *world) noOutputCarriesTheCredential() error {
	for _, executed := range m.everything {
		if strings.Contains(executed.stdout+executed.stderr, theCredential) {
			return fmt.Errorf("%q printed the credential", executed.command)
		}
	}
	return nil
}

// noPersistentLogCarriesTheCredential walks the whole HOME. The config file is
// where the operator wrote the credential themselves and is not part of what is
// being measured: what may not carry it is anything the binary writes.
func (m *world) noPersistentLogCarriesTheCredential() error {
	written := filepath.Join(m.home, ".roca", "config.toml")
	return filepath.Walk(m.home, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || path == written {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if strings.Contains(string(content), theCredential) {
			return fmt.Errorf("the credential ended up written at %s", path)
		}
		return nil
	})
}
