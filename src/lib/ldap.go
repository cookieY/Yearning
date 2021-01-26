package lib

import (
"Yearning-go/src/model"
"crypto/tls"
"errors"
"fmt"
"gopkg.in/ldap.v3"
)

func LdapConnenct(l *model.Ldap, user string, pass string, isTest bool) (isOk bool, err error) {

	var s string

	ld, err := ldap.Dial("tcp", l.Url)

	if l.Ldaps {
		if err := ld.StartTLS(&tls.Config{InsecureSkipVerify: true}); err != nil {
			return false, err
		}
	}

	if err != nil {
		return false, err
	}

	defer ld.Close()

	if ld != nil {
		if err := ld.Bind(l.User, l.Password); err != nil {
			return false, err
		}
		if isTest {
			return true, nil
		}

	}

	switch l.Type {
	case 1:
		s = fmt.Sprintf("(sAMAccountName=%s)", user)
	case 2:
		s = fmt.Sprintf("(uid=%s)", user)
	default:
		s = fmt.Sprintf("(cn=%s)", user)
	}

	searchRequest := ldap.NewSearchRequest(
		l.Sc,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		fmt.Sprintf("(&(objectClass=organizationalPerson)%s)", s),
		[]string{"dn"},
		nil,
	)

	sr, err := ld.Search(searchRequest)

	if err != nil {
		return false, err
	}

	if len(sr.Entries) != 1 {
		return false, errors.New("User does not exist or too many entries returned")
	}

	userdn := sr.Entries[0].DN

	if err := ld.Bind(userdn, pass); err != nil {
		return false, err
	}
	return true, nil
}

