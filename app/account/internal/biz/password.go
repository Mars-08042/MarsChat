package biz

import (
	"crypto/rand"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

// checkPasswordStrength 要求同时包含字母和数字。长度和字符集（可打印 ASCII，最多 64 位，
// 保证不超过 bcrypt 的 72 字节上限）由接口的 protovalidate 规则保证。
func checkPasswordStrength(pw string) error {
	var letter, digit bool
	for _, r := range pw {
		switch {
		case unicode.IsLetter(r):
			letter = true
		case unicode.IsDigit(r):
			digit = true
		}
	}
	if !letter || !digit {
		return ErrWeakPassword()
	}
	return nil
}

func hashPassword(pw string, cost int) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), cost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

func comparePassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}

// newDummyHash 生成一个与真实密码同样 cost 的哈希。手机号不存在时也用它比较一次，
// 让“手机号不存在”和“密码错误”的耗时相同，无法据此判断手机号是否注册。
func newDummyHash(cost int) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hashPassword(string(b), cost)
}
