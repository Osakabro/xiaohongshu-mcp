package xiaohongshu

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-rod/rod"
	"github.com/pkg/errors"
)

type LoginAction struct {
	page *rod.Page
}

func NewLogin(page *rod.Page) *LoginAction {
	return &LoginAction{page: page}
}

func (a *LoginAction) CheckLoginStatus(ctx context.Context) (bool, error) {
	// 加超时保护：只是查登录态的快速检查，不应无限挂（登录扫码的等待在 Login/WaitForLogin 里）
	pp := a.page.Context(ctx).Timeout(30 * time.Second)
	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()

	time.Sleep(1 * time.Second)

	exists, _, err := pp.Has(`.main-container .user .link-wrapper .channel`)
	if err != nil {
		return false, errors.Wrap(err, "check login status failed")
	}

	if !exists {
		return false, errors.Wrap(err, "login status element not found")
	}

	return true, nil
}

// CurrentUser 当前登录用户的基础信息。
type CurrentUser struct {
	Nickname string `json:"nickname"`
	UserID   string `json:"userId"`
}

// CurrentUser 从当前页面的 __INITIAL_STATE__ 读取登录用户信息。
// 需在 CheckLoginStatus 之后调用：复用已加载的 explore 页，不做额外导航。
func (a *LoginAction) CurrentUser(ctx context.Context) (*CurrentUser, error) {
	pp := a.page.Context(ctx).Timeout(10 * time.Second)

	res, err := pp.Eval(`() => {
		const u = window.__INITIAL_STATE__ && window.__INITIAL_STATE__.user;
		const info = u && u.userInfo && u.userInfo.value !== undefined ? u.userInfo.value : (u && u.userInfo);
		if (!info || info.guest) return "";
		return JSON.stringify({nickname: info.nickname, userId: info.userId || info.user_id});
	}`)
	if err != nil {
		return nil, errors.Wrap(err, "read current user state failed")
	}

	raw := res.Value.String()
	if raw == "" {
		return nil, errors.New("current user not found in page state")
	}

	var user CurrentUser
	if err := json.Unmarshal([]byte(raw), &user); err != nil {
		return nil, errors.Wrap(err, "unmarshal current user failed")
	}

	return &user, nil
}

func (a *LoginAction) Login(ctx context.Context) error {
	pp := a.page.Context(ctx)

	// 导航到小红书首页，这会触发二维码弹窗
	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()

	// 等待一小段时间让页面完全加载
	time.Sleep(2 * time.Second)

	// 检查是否已经登录
	if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
		// 已经登录，直接返回
		return nil
	}

	// 等待扫码成功提示或者登录完成
	// 这里我们等待登录成功的元素出现，这样更简单可靠
	pp.MustElement(".main-container .user .link-wrapper .channel")

	return nil
}

func (a *LoginAction) FetchQrcodeImage(ctx context.Context) (string, bool, error) {
	pp := a.page.Context(ctx)

	// 导航到小红书首页，这会触发二维码弹窗
	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()

	// 等待一小段时间让页面完全加载
	time.Sleep(2 * time.Second)

	// 检查是否已经登录
	if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
		return "", true, nil
	}

	// 获取二维码图片
	src, err := pp.MustElement(".login-container .qrcode-img").Attribute("src")
	if err != nil {
		return "", false, errors.Wrap(err, "get qrcode src failed")
	}
	if src == nil || len(*src) == 0 {
		return "", false, errors.New("qrcode src is empty")
	}

	return *src, false, nil
}

func (a *LoginAction) WaitForLogin(ctx context.Context) bool {
	pp := a.page.Context(ctx)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			el, err := pp.Element(".main-container .user .link-wrapper .channel")
			if err == nil && el != nil {
				return true
			}
		}
	}
}

// SendSmsCode 手机号登录第一步：打开登录弹窗（小红书网页默认就是手机号验证码方式），
// 填入手机号并点击「获取验证码」触发短信下发。调用方需保持这个 page 直到 SubmitSmsCode。
func (a *LoginAction) SendSmsCode(ctx context.Context, phone string) error {
	pp := a.page.Context(ctx).Timeout(40 * time.Second)
	pp.MustNavigate("https://www.xiaohongshu.com/explore").MustWaitLoad()
	time.Sleep(2 * time.Second)

	// 已登录则无需再登录
	if exists, _, _ := pp.Has(".main-container .user .link-wrapper .channel"); exists {
		return errors.New("当前已登录，无需再登录")
	}

	// 填手机号（弹窗默认就是手机号登录：placeholder="输入手机号"）
	phoneInput, err := pp.Element(`input[placeholder="输入手机号"]`)
	if err != nil {
		return errors.Wrap(err, "未找到手机号输入框（登录弹窗未出现或结构变化）")
	}
	phoneInput.MustInput(phone)

	// 点「获取验证码」（是文字元素，非标准 button）
	clicked := pp.MustEval(`() => {
		const e = [...document.querySelectorAll('span,div,a,button')].find(x => x.children.length===0 && x.textContent.trim()==='获取验证码');
		if (e) { e.click(); return true; }
		return false;
	}`).Bool()
	if !clicked {
		return errors.New("未找到「获取验证码」按钮")
	}
	return nil
}

// SubmitSmsCode 手机号登录第二步：在已打开的登录弹窗填入短信验证码，点击「登录」，等待登录成功。
func (a *LoginAction) SubmitSmsCode(ctx context.Context, code string) error {
	pp := a.page.Context(ctx).Timeout(60 * time.Second)

	codeInput, err := pp.Element(`input[placeholder="输入验证码"]`)
	if err != nil {
		return errors.Wrap(err, "未找到验证码输入框（登录会话可能已失效，请重新 send_sms_code）")
	}
	codeInput.MustInput(code)

	// 点「登录」（button.submit）
	loginBtn, err := pp.Element(`button.submit`)
	if err != nil {
		return errors.Wrap(err, "未找到登录按钮")
	}
	loginBtn.MustClick()

	// 等待登录成功（最多 20s）
	ctxTimeout, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if !a.WaitForLogin(ctxTimeout) {
		return errors.New("登录未成功（验证码错误/过期，或触发了额外的图形/滑块验证）")
	}
	return nil
}
