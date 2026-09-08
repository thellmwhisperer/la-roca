package service

type Attempt struct {
	Name    string `json:"provider"`
	Ready   bool   `json:"ready"`
	ModelID string `json:"model,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Action  string `json:"action,omitempty"`
}
