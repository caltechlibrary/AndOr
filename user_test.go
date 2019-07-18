//
// Package andor provides support for building simple digital
// object repositories in Go where objects are stored in a
// dataset collection and the UI of the repository is static
// HTML 5 documents using JavaScript to access a web API.
//
// @Author R. S. Doiel, <rsdoiel@library.caltech.edu>
//
package andor

import (
	"io/ioutil"
	"testing"
	// Caltech Library packages
)

func TestLoadUser(t *testing.T) {
	UsersTOML = "test_users.toml"
	UsersTOMLSrc := []byte(`
#
# Example Test users file for testing 
# LoadUser()
#

# User id
["jane.doe@example.edu"]
# Display name
display_name = "Jane Doe"
# By default objects are create in this queue
create_queue = "deposit"
# Jane is a member of the "deposit" workflow/queue
member_of = ["deposit"]
`)
	err := ioutil.WriteFile(UsersTOML, UsersTOMLSrc, 0666)
	if err != nil {
		t.Errorf("%s, %s", UsersTOML, err)
		t.FailNow()
	}
	if _, err := LoadUser(UsersTOML); err != nil {
		t.Errorf("%s", err)
		t.FailNow()
	}
}

func TestUserToBytes(t *testing.T) {
	t.Errorf("TestUserToBytes() not implemented")
}

func TestUserToString(t *testing.T) {
	t.Errorf("TestUserToString() not implemented")
}

func TestListUsers(t *testing.T) {
	t.Errorf("TestListUsers() not implemented")
}

func TestRemoveUser(t *testing.T) {
	t.Errorf("TestRemoveUser() not implemented")
}
