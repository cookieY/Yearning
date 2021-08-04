package lib

import (
	"Yearning-go/src/model"
	"crypto/tls"
	"errors"
	"fmt"
	"gopkg.in/ldap.v3"
)

func LdapContent(l *model.Ldap, user string, pass string, isTest bool) (isOk bool, err error) {

	var ld *ldap.Conn

	if l.Ldaps {
		ld, err = ldap.DialTLS("tcp", l.Url, &tls.Config{InsecureSkipVerify: true})
	} else {
		ld, err = ldap.Dial("tcp", l.Url)
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

	searchRequest := ldap.NewSearchRequest(
		l.Sc,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		fmt.Sprintf(l.Type, user),
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
