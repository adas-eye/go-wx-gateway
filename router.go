/**
 * REST API router
 * Rosbit Xu
 */
package main

import (
	"github.com/urfave/negroni"
	"github.com/gernest/alien"
	"net/http"
	"fmt"
	"os"
	"strconv"
	"encoding/json"
	"github.com/rosbit/go-wx-api"
	"github.com/rosbit/go-wx-api/conf"
	"github.com/rosbit/go-wx-api/msg"
	"github.com/rosbit/go-wx-api/auth"
	"github.com/rosbit/go-wx-api/tools"
	"wx-gateway/conf"
	"wx-gateway/handlers"
)

func StartWxGateway() error {
	api := negroni.New()
	api.Use(negroni.NewRecovery())
	api.Use(negroni.NewLogger())

	router := alien.New()
	serviceConf := gwconf.ServiceConf
	for _, service := range serviceConf.Services {
		paramConf := service.WxParams
		wxParams, err := wxconf.NewWxParams(paramConf.Token, paramConf.AppId, paramConf.AppSecret, paramConf.AesKey)
		if err != nil {
			return fmt.Errorf("failed to init servie %s: %v", service.Name, err)
		}
		wxParamsCache[service.Name] = wxParams

		// init wx API
		wxService := wxapi.InitWxAPIWithParams(wxParams, service.WorkerNum, os.Stdout)
		endpoints := service.Endpoints

		// add uri signature checker
		signatureChecker := wxapi.NewWxSignatureChecker(paramConf.Token, service.Timeout, []string{endpoints.ServicePath})
		api.Use(negroni.HandlerFunc(signatureChecker))

		// set router
		router.Get(endpoints.ServicePath,  wxService.Echo)
		router.Post(endpoints.ServicePath, wxService.Request)
		if len(endpoints.RedirectPath) > 0 {
			router.Get(endpoints.RedirectPath, wxService.Redirect)
		}

		// set msg handlers and menu redirector
		if service.MsgProxyPass != "" {
			msgHandler := gwhandlers.NewMsgHandler(service.MsgProxyPass, wxParams, serviceConf.DontAppendUserInfo)
			wxService.RegisterWxMsghandler(msgHandler)
		} else {
			wxService.RegisterWxMsghandler(wxmsg.MsgHandler)
		}

		if len(service.RedirectURL) > 0 {
			if len(endpoints.RedirectPath) == 0 {
				return fmt.Errorf("listen-endpoints/redirect-path in servie %s must be specfied if you want to use redirect-url", service.Name)
			}
			wxService.RegisterRedirectUrl(service.RedirectURL, service.RedirectUserInfoFlag)
		}
	}

	commonEndpoints := &serviceConf.CommonEndpoints
	if commonEndpoints.HealthCheck != "" {
		router.Get(commonEndpoints.HealthCheck, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "OK\n")
		})
	}
	if commonEndpoints.WxQr != "" {
		router.Get(commonEndpoints.WxQr, createWxQr)
	}
	if commonEndpoints.WxUser != "" {
		router.Get(commonEndpoints.WxUser, getWxUserInfo)
	}
	if commonEndpoints.SnsAPI != "" {
		router.Get(commonEndpoints.SnsAPI, snsAPI)
	}
	if commonEndpoints.ShortUrl != "" {
		router.Post(commonEndpoints.ShortUrl, createShorturl)
	}
	api.UseHandler(router)

	if serviceConf.TokenCacheDir != "" {
		wxconf.TokenStorePath = serviceConf.TokenCacheDir
	}

	listenParam := fmt.Sprintf("%s:%d", serviceConf.ListenHost, serviceConf.ListenPort)
	fmt.Printf("%v\n", http.ListenAndServe(listenParam, api))
	return nil
}

var wxParamsCache = map[string]*wxconf.WxParamsT{}

// GET ${commonEndpoints.WxQr}?s=<service-name-in-conf>&t=<type-name,temp|forever>[&sceneid=xx][&e=<expire-secs-for-type-temp>]
func createWxQr(w http.ResponseWriter, r *http.Request) {
	service := r.FormValue("s")
	if service == "" {
		writeError(w, http.StatusBadRequest, "s(ervice) parameter expected")
		return
	}

	wxParams, ok := wxParamsCache[service]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown service name %s", service))
		return
	}

	qrType := r.FormValue("t")
	if qrType == "" {
		writeError(w, http.StatusBadRequest, "t(type) parameter expected")
		return
	}
	switch qrType {
	case "temp", "forever":
	default:
		writeError(w, http.StatusBadRequest, `t(ype) value must be "temp" or "forever"`)
		return
	}

	sceneid := r.FormValue("sceneid")
	if sceneid == "" {
		sceneid = "0"
	}

	accessToken, err := wxauth.NewAccessTokenWithParams(wxParams).Get()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var ticketURL2ShowQrCode, urlIncluedInQrcode string
	switch qrType {
	case "temp":
		expireSecs := 30
		e := r.FormValue("e")
		if e == "" {
			expireSecs, _ := strconv.Atoi(e)
			if expireSecs <= 0 {
				expireSecs = 30
			}
		}
		ticketURL2ShowQrCode, urlIncluedInQrcode, err = wxtools.CreateTempQrStrScene(accessToken, sceneid, expireSecs)
	case "forever":
		ticketURL2ShowQrCode, urlIncluedInQrcode, err = wxtools.CreateQrStrScene(accessToken, sceneid)
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJson(w, http.StatusOK, map[string]interface{}{
		"code": http.StatusOK,
		"msg": "OK",
		"result": map[string]string {
			"ticketURL2ShowQrCode": ticketURL2ShowQrCode,
			"urlIncluedInQrcode": urlIncluedInQrcode,
		},
	})
}

// GET ${commonEndpoints.WxUser}?s=<service-name-in-conf>&o=<openId>
func getWxUserInfo(w http.ResponseWriter, r *http.Request) {
	service := r.FormValue("s")
	if service == "" {
		writeError(w, http.StatusBadRequest, "s(ervice) parameter expected")
		return
	}

	wxParams, ok := wxParamsCache[service]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown service name %s", service))
		return
	}

	openId := r.FormValue("o")
	if openId == "" {
		writeError(w, http.StatusBadRequest, "o(penId) parameter expected")
		return
	}

	userInfo, err := gwhandlers.GetUserInfo(wxParams, openId)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJson(w, http.StatusOK, map[string]interface{}{
		"code": http.StatusOK,
		"msg": "OK",
		"userInfo": userInfo,
	})
}

// GET ${commonEndpoints.SnsAPI}?s=<service-name-in-conf>&code=<code-from-wx-server>&scope={userinfo|base}
func snsAPI(w http.ResponseWriter, r *http.Request) {
	service := r.FormValue("s")
	if service == "" {
		writeError(w, http.StatusBadRequest, "s(ervice) parameter expected")
		return
	}

	wxParams, ok := wxParamsCache[service]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown service name %s", service))
		return
	}

	scope := r.FormValue("scope")
	switch scope {
	case "userinfo","base":
	case "", "snsapi_base":
		scope = "base"
	case "snsapi_userinfo":
		scope = "userinfo"
	default:
		writeError(w, http.StatusBadRequest, `scope must be "useinfo", "base", "sns_userinfo" or "sns_base"`)
		return
	}

	code := r.FormValue("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "code parameter expected")
		return
	}

	wxUser := wxauth.NewWxUser(wxParams)
	openId, err := wxUser.GetOpenId(code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var userInfo map[string]interface{}
	if scope == "base" {
		userInfo, err = wxUser.GetInfoByAccessToken()
	} else {
		if err = wxUser.GetInfo(); err == nil {
			userInfo = wxUser.UserInfo
		}
	}

	writeJson(w, http.StatusOK, map[string]interface{}{
		"code": http.StatusOK,
		"msg": "OK",
		"openId": openId,
		"userInfo": userInfo,
		"error": func()string{if err == nil {return ""}; return err.Error()}(),
	})
}

// POST ${commonEndpoints.ShortUrl}
// s=<service-name-in-conf>&u=<long-url>
func createShorturl(w http.ResponseWriter, r *http.Request) {
	service := r.FormValue("s")
	if service == "" {
		writeError(w, http.StatusBadRequest, "s(ervice) parameter expected")
		return
	}

	wxParams, ok := wxParamsCache[service]
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown service name %s", service))
		return
	}

	longUrl := r.FormValue("u")
	if longUrl == "" {
		writeError(w, http.StatusBadRequest, "u(rl) parameter expected")
		return
	}

	accessToken, err := wxauth.NewAccessTokenWithParams(wxParams).Get()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	shortUrl, err := wxtools.MakeShorturl(accessToken, longUrl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJson(w, http.StatusOK, map[string]interface{}{
		"code": http.StatusOK,
		"msg": "OK",
		"short-url": shortUrl,
	})
}

func writeJson(w http.ResponseWriter, code int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.Encode(data)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJson(w, code, map[string]interface{}{"code": code, "msg": msg})
}
