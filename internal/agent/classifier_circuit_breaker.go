package agent

// CircuitBreaker tracks classifier denials and triggers an interruption when
// the agent is repeatedly attempting blocked actions.
//
// Translated from Codex's GuardianRejectionCircuitBreaker in
// codex-rs/core/src/guardian/mod.rs.
//
// Rules:
//   - 3 consecutive denials → interrupt turn (escalate to user)
//   - 10 denials in a 50-action sliding window → interrupt turn
//   - Once interrupted, stays interrupted for the remainder of the turn
type CircuitBreaker struct {
	turns map[string]*circuitBreakerTurn
}

type circuitBreakerTurn struct {
	consecutiveDenials int
	recentDenials      []bool // sliding window, max 50 entries
	interruptTriggered bool
}

const (
	// maxConsecutiveClassifierDenials is the max consecutive denials before interrupt.
	maxConsecutiveClassifierDenials = 3
	// maxRecentClassifierDenials is the max denials in the window before interrupt.
	maxRecentClassifierDenials = 10
	// classifierDenialWindowSize is the sliding window size for recent denials.
	classifierDenialWindowSize = 50
)

// NewCircuitBreaker creates a new circuit breaker.
func NewCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		turns: make(map[string]*circuitBreakerTurn),
	}
}

// RecordDenial records a classifier denial for the given turn.
// Returns true if the circuit breaker has triggered, meaning the turn should
// be interrupted and escalated to the user.
func (cb *CircuitBreaker) RecordDenial(turnID string) bool {
	if cb == nil {
		return false
	}
	turn := cb.getOrCreate(turnID)
	turn.consecutiveDenials++
	turn.recentDenials = append(turn.recentDenials, true)
	if len(turn.recentDenials) > classifierDenialWindowSize {
		turn.recentDenials = turn.recentDenials[len(turn.recentDenials)-classifierDenialWindowSize:]
	}

	recentDenials := countTrue(turn.recentDenials)

	if !turn.interruptTriggered &&
		(turn.consecutiveDenials >= maxConsecutiveClassifierDenials ||
			recentDenials >= maxRecentClassifierDenials) {
		turn.interruptTriggered = true
		return true
	}
	return false
}

// RecordNonDenial records an allow or warn decision, resetting the consecutive
// denial counter.
func (cb *CircuitBreaker) RecordNonDenial(turnID string) {
	if cb == nil {
		return
	}
	turn := cb.getOrCreate(turnID)
	turn.consecutiveDenials = 0
	turn.recentDenials = append(turn.recentDenials, false)
	if len(turn.recentDenials) > classifierDenialWindowSize {
		turn.recentDenials = turn.recentDenials[len(turn.recentDenials)-classifierDenialWindowSize:]
	}
}

// IsInterrupted returns true if the circuit breaker has triggered.
func (cb *CircuitBreaker) IsInterrupted(turnID string) bool {
	if cb == nil {
		return false
	}
	turn, ok := cb.turns[turnID]
	if !ok {
		return false
	}
	return turn.interruptTriggered
}

// ClearTurn clears the circuit breaker state for a turn.
func (cb *CircuitBreaker) ClearTurn(turnID string) {
	if cb == nil {
		return
	}
	delete(cb.turns, turnID)
}

func (cb *CircuitBreaker) getOrCreate(turnID string) *circuitBreakerTurn {
	turn, ok := cb.turns[turnID]
	if !ok {
		turn = &circuitBreakerTurn{}
		cb.turns[turnID] = turn
	}
	return turn
}

func countTrue(vals []bool) int {
	n := 0
	for _, v := range vals {
		if v {
			n++
		}
	}
	return n
}
