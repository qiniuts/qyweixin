package access_token

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/golang/glog"
	"github.com/pkg/errors"

	"github.com/lenye/qyweixin/internal/http"
)

const (
	retryInterval = 5 * time.Second //每隔5秒重试
	tokenURL      = "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=%s&corpsecret=%s"
)

//凭证
type AccessToken struct {
	AccessToken string `json:"access_token"` //获取到的凭证
	ExpiresIn   int64  `json:"expires_in"`   //凭证有效时间，单位：秒
	NextGet     int64  //下次取凭证时间
}

type WeiXinQyAccessTokenEventChan struct {
	AccessToken string
	Err         error
}

type AccessTokenClient struct {
	ticket   atomic.Value
	Client   *http.HttpClient
	QuitChan chan int
}

func NewAccessTokenClient(connectTimeout time.Duration, requestTimeout time.Duration) *AccessTokenClient {
	p := &AccessTokenClient{
		Client:   http.NewHttpClient(connectTimeout, requestTimeout),
		QuitChan: make(chan int),
	}
	p.Client.HttpDump = true
	p.SwapTicket(&AccessToken{})
	return p
}

//Load
func (p *AccessTokenClient) Load() *AccessToken {
	return p.ticket.Load().(*AccessToken)
}

//swapTicket
func (p *AccessTokenClient) SwapTicket(ticket *AccessToken) {
	p.ticket.Store(ticket)
}

//getAccessToken
func (p *AccessTokenClient) getAccessToken(appId, appSecret string) (*AccessToken, error) {
	respBody, err := p.Client.HTTPGet(fmt.Sprintf(tokenURL, appId, appSecret))
	if err != nil {
		return nil, errors.Wrap(err, "getAccessToken HTTPGet")
	}

	accessToken := p.Load()

	var newAccessToken AccessToken
	err = json.Unmarshal(respBody, &newAccessToken)
	if err != nil {
		accessToken.AccessToken = ""
		accessToken.ExpiresIn = 0
		p.SwapTicket(accessToken)
		return accessToken, errors.Wrap(err, "getAccessToken json.Unmarshal")
	}

	//刷新策略
	switch {
	case newAccessToken.ExpiresIn >= 60*60:
		newAccessToken.NextGet = newAccessToken.ExpiresIn - 30*60
	case newAccessToken.ExpiresIn >= 30*60:
		newAccessToken.NextGet = newAccessToken.ExpiresIn - 10*60
	case newAccessToken.ExpiresIn >= 10*60:
		newAccessToken.NextGet = newAccessToken.ExpiresIn - 60
	default:
		newAccessToken.NextGet = newAccessToken.ExpiresIn
	}

	glog.Infof("old access_token=%+v", accessToken)
	glog.Infof("new access_token=%+v", newAccessToken)

	accessToken.AccessToken = newAccessToken.AccessToken
	accessToken.ExpiresIn = newAccessToken.ExpiresIn
	accessToken.NextGet = newAccessToken.NextGet

	p.SwapTicket(accessToken)
	return accessToken, nil
}

//Loop
func (p *AccessTokenClient) Loop(appId, appSecret string) {
	var refreshInterval time.Duration

	newAccessToken, err := p.getAccessToken(appId, appSecret)
	if err != nil {
		glog.Error(err)
		refreshInterval = retryInterval
	} else {
		refreshInterval = time.Duration(newAccessToken.NextGet) * time.Second
	}
	glog.Infof("next access token time.NewTicker=%v", refreshInterval)
	timeTicker := time.NewTicker(refreshInterval)

	for {
		select {
		case <-timeTicker.C:
			newAccessToken, err := p.getAccessToken(appId, appSecret)
			if err != nil {
				glog.Error(err)
				refreshInterval = retryInterval
			} else {
				refreshInterval = time.Duration(newAccessToken.NextGet) * time.Second
			}
			glog.Infof("next access token time.NewTicker=%v", refreshInterval)
			timeTicker.Stop()
			timeTicker = time.NewTicker(refreshInterval)
		case <-p.QuitChan:
			goto exit
		}
	}

exit:
	glog.Info("exiting access token Loop")
	timeTicker.Stop()
}
