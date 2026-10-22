package account

import (
	"golang.org/x/crypto/bcrypt"

	"github.com/royvandam/pigeon/internal/token"
)

// DefaultPasswordCost is the bcrypt cost for stored passwords. Tests use
// bcrypt.MinCost, because every fixture hashes passwords.
const DefaultPasswordCost = bcrypt.DefaultCost

// passwordHasher hashes and checks passwords with bcrypt at one cost. Checking
// an unknown username compares against a dummy hash of the same cost, so it
// takes as long as a wrong password for an existing account.
type passwordHasher struct {
	cost  int
	dummy []byte
}

func newPasswordHasher(cost int) (passwordHasher, error) {
	secret, err := token.New()
	if err != nil {
		return passwordHasher{}, err
	}
	dummy, err := bcrypt.GenerateFromPassword([]byte(secret), cost)
	if err != nil {
		return passwordHasher{}, err
	}
	return passwordHasher{cost: cost, dummy: dummy}, nil
}

func (h passwordHasher) hash(password string) ([]byte, error) {
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	return bcrypt.GenerateFromPassword([]byte(password), h.cost)
}

// matches reports whether password matches hash. A nil hash, for an unknown
// username, is checked against the dummy hash and never matches.
func (h passwordHasher) matches(hash []byte, password string) bool {
	if hash == nil {
		bcrypt.CompareHashAndPassword(h.dummy, []byte(password))
		return false
	}
	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
}
