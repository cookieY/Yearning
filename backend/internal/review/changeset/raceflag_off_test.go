//go:build !race

package changeset

// Window tail element is unterminated: continue splitting using the completed token prefix; after EOF, the remaining tail is adjudicated by cutTail (fail closed if unprovable).
const raceEnabled = false
