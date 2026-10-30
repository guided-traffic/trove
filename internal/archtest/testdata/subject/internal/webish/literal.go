package webish

import "example.test/fix/internal/authz"

func viaLiteral() { _ = authz.Subject{Name: "bob"}; _ = &authz.Subject{} }
