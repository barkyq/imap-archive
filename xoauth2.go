package main

import (
	"context"
	"fmt"

	"github.com/emersion/go-sasl"
	"golang.org/x/oauth2"
)

type xOAuth2 struct {
	useremail string
	config    *oauth2.Config
	token     *oauth2.Token
}

func XOAuth2(useremail string, config *oauth2.Config, token *oauth2.Token) sasl.Client {
	return &xOAuth2{useremail, config, token}
}

func (a *xOAuth2) Start() (string, []byte, error) {
	tsrc := (a.config).TokenSource(context.Background(), a.token)
	if t, err := tsrc.Token(); err == nil {
		str := fmt.Sprintf("user=%sauth=Bearer %s", a.useremail, t.AccessToken)
		resp := []byte(str)
		return "XOAUTH2", resp, nil
	} else {
		return "", []byte{}, err
	}
}

func (a *xOAuth2) Next(fromServer []byte) ([]byte, error) {
	return nil, fmt.Errorf("unexpected server challenge")
}

func Gmail_Generate_Token(client_id, client_secret, refresh_token string) (*oauth2.Config, *oauth2.Token) {
	config := &oauth2.Config{
		ClientID:     client_id,
		ClientSecret: client_secret,
		Endpoint: oauth2.Endpoint{
			AuthURL:   "https://accounts.google.com/o/oauth2/auth",
			TokenURL:  "https://oauth2.googleapis.com/token",
			AuthStyle: 0,
		},
		RedirectURL: "https://localhost",
		Scopes:      []string{"https://mail.google.com/"},
	}
	token := &oauth2.Token{
		TokenType:    "Bearer",
		RefreshToken: refresh_token,
	}
	return config, token
}

func Outlook_Generate_Token(client_id, refresh_token string) (*oauth2.Config, *oauth2.Token) {
	config := &oauth2.Config{
		ClientID: client_id,
		Endpoint: oauth2.Endpoint{
			AuthURL:   "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			TokenURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/token",
			AuthStyle: oauth2.AuthStyleAutoDetect,
		},
		RedirectURL: "https://localhost:8080",
		Scopes:      []string{"offline_access", "https://outlook.office365.com/IMAP.AccessAsUser.All", "https://outlook.office365.com/SMTP.Send"},
	}
	token := &oauth2.Token{
		TokenType:    "Bearer",
		RefreshToken: refresh_token,
	}
	return config, token
}
