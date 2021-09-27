/*
 * Copyright 2018 Ji-Young Park(jiyoung.park.dev@gmail.com)
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package core

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/jparklab/synology-csi/pkg/synology/options"

	"github.com/stretchr/testify/assert"
)

/************************************************************
 * Tests
 ************************************************************/
// Tests if a request fails if the session is not logged in
func TestSessionNotLoggedIn(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		resp.Write([]byte(""))
	}))
	defer testServer.Close()

	baseURL := fmt.Sprintf("%s/webapi", testServer.URL)
	session := NewSession(baseURL, "Core")

	_, err := session.Get("dummy", url.Values{})
	assert.EqualError(t, err, "Session has not been logged in yet")
}

func TestSessionLogin(t *testing.T) {

	testServer := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		params := req.URL.Query()

		switch req.URL.Path {
		case "/webapi/auth.cgi":
			{
				assert.Equal(t, "login", params.Get("method"))
				resp.Write([]byte(`{ "data": { "sid": "test_sid" }, "success": true }`))
			}
		case "/webapi/entry.cgi":
			{
				assert.Equal(t, "SYNO.Core.Security.DSM", params.Get("api"))
				resp.Write([]byte(`{ "data": { "timeout": 10 }, "success": true }`))
			}
		}
	}))

	defer testServer.Close()

	baseURL := fmt.Sprintf("%s/webapi", testServer.URL)
	s := NewSession(baseURL, "Core")

	// test login
	so := options.NewSynologyOptions()
	so.Username = "username"
	so.Password = "password"
	sid, err := s.Login(&so)

	assert.NoError(t, err)
	assert.Equal(t, "test_sid", sid)
	assert.Equal(t, 10, s.(*session).timeoutMinute)
}

func TestAPIEntry(t *testing.T) {
	sid := "test_sid_entry"

	testServer := httptest.NewServer(http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
		params := req.URL.Query()
		switch req.URL.Path {
		case "/webapi/auth.cgi":
			{
				method := params.Get("method")
				if method == "login" {
					fmt.Fprintf(resp, `{ "data": { "sid": "%s" }, "success": true }`, sid)
				} else {
					assert.Equal(t, "SYNO.Core.Security.DSM", params.Get("api"))
					resp.Write([]byte(`{
						"timeout": 1
					}`))
				}
			}
		case "/webapi/entry.cgi":
			{
				switch params.Get("api") {
				case "SYNO.Core.Security.DSM":
					resp.Write([]byte(`{ "data": { "timeout": 1 }, "success": true }`))
				case "TestAPI":
					assert.Equal(t, sid, params.Get("_sid"))
					assert.Equal(t, "sample", params.Get("name"))

					resp.Write([]byte(`{ "data": { "value": "value_1" }, "success": true }`))
				}
			}
		}
	}))

	defer testServer.Close()

	baseURL := fmt.Sprintf("%s/webapi", testServer.URL)
	s := NewSession(baseURL, "Core")

	so := options.NewSynologyOptions()
	so.Username = "username"
	so.Password = "password"
	s.Login(&so)

	api := NewAPIEntry(s, "entry.cgi", "TestAPI", "1")

	resp, err := api.Get("list", url.Values{
		"name": {"sample"},
	})

	assert.NoError(t, err)
	assert.Equal(t, `"value_1"`, string(*resp["value"]))
}
