package lib

import (
	"reflect"
	"testing"
)

func TestCheckPasswordFromAuth(t *testing.T) {
	type args struct {
		email    string
		password string
	}
	tests := []struct {
		name    string
		args    args
		notwant string
	}{
		{
			name: "bingo",
			args: args{
				email:    "penzai@fordeal.com",
				password: "For123deal",
			},
			notwant: "",
		},
		{
			name: "fuck",
			args: args{
				email:    "penzai@fordeal.com",
				password: "xxx",
			},
			notwant: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CheckPasswordFromAuth(tt.args.email, tt.args.password); got == tt.notwant {
				t.Errorf("CheckPasswordFromAuth() = %v, want %v", got, tt.notwant)
			}
		})
	}
}

func TestWhoAmI(t *testing.T) {
	type args struct {
		idtoken string
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{
			name: "bingo",
			args: args{
				idtoken: "eyJhbGciOiJSUzI1NiIsImtpZCI6ImJmZDBhYWQ0NmFiYjJmY2NkMmQ3YTMyNGY3MDIyYzg5In0.eyJpc3MiOiJodHRwOi8vYXV0aC5kdW9sYWluYy5jb20vLndlbGwta25vd24vb3BlbmlkLWNvbmZpZ3VyYXRpb24iLCJzdWIiOiI0MjY0IiwiYXVkIjoib2F1dGgiLCJleHAiOjE1ODY0NjAyNjYsImlhdCI6MTU4NjQxNzA2NiwibmFtZSI6IlBlblphaSIsImVtYWlsIjoicGVuemFpQGZvcmRlYWwuY29tIiwiZW1wbG95ZWVfaWQiOjQyNjQsImFjY291bnRfdHlwZSI6ImNodWFuZ3dhaSIsIm5pY2tfbmFtZSI6Ilx1NzZjNlx1NjgzZCJ9.Bed0U45CHwUujt-o-lv3ziB8mNqVuNju0Nq7GlzO0Oij5p_sPGPpDWNvsxwe3pWiIbPfnun8OwJe_RuRetEYgH4Mf5KB1WdPhZYFH-BXW-xetoxad-FFHIhjceBA2YHBIYxThZOjy2g651vagJBY6nh0jCzSsERbeH3p7pEjwzAJmTiG-hXinJn42GlIAbouAC0aOFcp6-cYXNgoGje9G96xlnTl46T1vrGiD1DtGiAsLruV7nx_8PTcjDimVxyLkrPsAYqgvWPgVSg91NVQlG_pEj3n4ut6C0tQW8aK1kzkUBe5UUbYQy5DNywT2VZkXU_dHVEG-DW-oHldxfOdTQ",
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WhoAmI(tt.args.idtoken); !reflect.DeepEqual(got.ErrorCode, tt.want) {
				t.Errorf("WhoAmI() = %v, want %v", got.ErrorCode, 0)
			}
		})
	}
}

func TestAuthCheckPassword(t *testing.T) {
	type args struct {
		password string
		hashed   string
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "bingo",
			args: args{
				password: "123456",
				hashed:   "$2b$12$pXM6DyDXEbJfkElYwMTrEuLgmmtzd1mNb3zruH95W8PBtnUaLlmI.",
			},
			want: true,
		},
		{
			name: "bingo",
			args: args{
				password: "1237456",
				hashed:   "$2b$12$pXM6DyDXEbJfkElYwMTrEuLgmmtzd1mNb3zruH95W8PBtnUaLlmI.",
			},
			want: false,
		},
		{
			name: "bingo",
			args: args{
				password: "123456",
				hashed:   "$2xxxb$12$pXM6DyDXEbJfkElYwMTrEuLgmmtzd1mNb3zruH95W8PBtnUaLlmI.",
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AuthCheckPassword(tt.args.password, tt.args.hashed); got != tt.want {
				t.Errorf("AuthCheckPassword() = %v, want %v", got, tt.want)
			}
		})
	}
}
