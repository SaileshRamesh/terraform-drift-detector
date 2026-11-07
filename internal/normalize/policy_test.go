package normalize

import "testing"

func TestPolicyJSONEquivalence(t *testing.T) {
	a := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	b := `{
	  "Statement": {"Action": ["sts:AssumeRole"], "Effect": "Allow", "Principal": {"Service": ["ec2.amazonaws.com"]}},
	  "Version": "2012-10-17"
	}`
	if PolicyJSON(a) != PolicyJSON(b) {
		t.Fatalf("expected equal:\n%s\n%s", PolicyJSON(a), PolicyJSON(b))
	}

	c := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	if PolicyJSON(a) == PolicyJSON(c) {
		t.Fatal("different principals must not compare equal")
	}

	d := `{"Statement":[{"Action":["s3:PutObject","s3:GetObject"]}]}`
	e := `{"Statement":[{"Action":["s3:GetObject","s3:PutObject"]}]}`
	if PolicyJSON(d) != PolicyJSON(e) {
		t.Fatal("action order must not matter")
	}
}
