package webish

import "example.test/fix/internal/authz"

type subjectAlias = authz.Subject

func viaTypeAlias() { _ = subjectAlias{} }
