package common

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// [FORK] 批量重置用的一次性密码：固定 8 位，必含大写字母、小写字母与数字。
// 字符集是完整的 62 字符（不过滤 0/O、1/l/I 等易混字符）——密码由管理员经手转交，
// 不做人眼分辨优化；过滤字符集只会降低熵。后端生成，从不接受调用方传入。
const (
	generatedAccountPasswordLength = 8
	requiredPasswordCharClasses    = 3
	passwordUpperChars             = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	passwordLowerChars             = "abcdefghijklmnopqrstuvwxyz"
	passwordDigitChars             = "0123456789"
	passwordAllChars               = passwordUpperChars + passwordLowerChars + passwordDigitChars
)

// GenerateRandomAccountPassword returns an 8-character password from crypto/rand
// that always contains an upper-case letter, a lower-case letter and a digit.
// Entropy is about 47 bits (8 x log2(62)); that is the ceiling of the product
// requirement and is recorded as a known trade-off in the design document.
func GenerateRandomAccountPassword() (string, error) {
	password := make([]byte, generatedAccountPasswordLength)
	// 三类字符各占一位，保证一定出现；其余 5 位从全字符集补足。
	for i, charset := range []string{passwordUpperChars, passwordLowerChars, passwordDigitChars} {
		index, err := randomPasswordIndex(len(charset))
		if err != nil {
			return "", err
		}
		password[i] = charset[index]
	}
	for i := requiredPasswordCharClasses; i < generatedAccountPasswordLength; i++ {
		index, err := randomPasswordIndex(len(passwordAllChars))
		if err != nil {
			return "", err
		}
		password[i] = passwordAllChars[index]
	}
	// Fisher–Yates：让三类必现字符的位置也随机，否则前 3 位永远是「大写、小写、数字」。
	for i := len(password) - 1; i > 0; i-- {
		j, err := randomPasswordIndex(i + 1)
		if err != nil {
			return "", err
		}
		password[i], password[j] = password[j], password[i]
	}
	return string(password), nil
}

// randomPasswordIndex returns a value in [0, n) without modulo bias.
func randomPasswordIndex(n int) (int, error) {
	if n <= 0 {
		return 0, fmt.Errorf("invalid password character set size %d", n)
	}
	value, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	return int(value.Int64()), nil
}
