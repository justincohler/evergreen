package send

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mongodb/grip/level"
	"github.com/mongodb/grip/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

type SenderSuite struct {
	senders map[string]Sender
	rand    *rand.Rand
	tempDir string
	suite.Suite
}

func TestSenderSuite(t *testing.T) {
	suite.Run(t, new(SenderSuite))
}

func (s *SenderSuite) SetupSuite() {
	var err error
	s.rand = rand.New(rand.NewSource(time.Now().Unix()))
	s.tempDir, err = ioutil.TempDir("", fmt.Sprintf("%v", s.rand))
	s.Require().NoError(err)
}

func (s *SenderSuite) SetupTest() {
	l := LevelInfo{level.Info, level.Notice}
	s.senders = map[string]Sender{
		"slack": &slackJournal{Base: NewBase("slack")},
		"xmpp":  &xmppLogger{Base: NewBase("xmpp")},
		"buildlogger": &buildlogger{
			Base: NewBase("buildlogger"),
			conf: &BuildloggerConfig{Local: MakeNative()},
		},
	}

	internal := new(InternalSender)
	internal.name = "internal"
	internal.output = make(chan *InternalMessage)
	s.senders["internal"] = internal

	native, err := NewNativeLogger("native", l)
	s.Require().NoError(err)
	s.senders["native"] = native

	nativeErr, err := NewErrorLogger("error", l)
	s.Require().NoError(err)
	s.senders["error"] = nativeErr

	nativeFile, err := NewFileLogger("native-file", filepath.Join(s.tempDir, "file"), l)
	s.Require().NoError(err)
	s.senders["native-file"] = nativeFile

	callsite, err := NewCallSiteConsoleLogger("callsite", 1, l)
	s.Require().NoError(err)
	s.senders["callsite"] = callsite

	callsiteFile, err := NewCallSiteFileLogger("callsite", filepath.Join(s.tempDir, "cs"), 1, l)
	s.Require().NoError(err)
	s.senders["callsite-file"] = callsiteFile

	stream, err := NewStreamLogger("stream", &bytes.Buffer{}, l)
	s.Require().NoError(err)
	s.senders["stream"] = stream

	jsons, err := NewJSONConsoleLogger("json", LevelInfo{level.Info, level.Notice})
	s.Require().NoError(err)
	s.senders["json"] = jsons

	jsonf, err := NewJSONFileLogger("json", filepath.Join(s.tempDir, "js"), l)
	s.Require().NoError(err)
	s.senders["json"] = jsonf

	var sender Sender
	multiSenders := []Sender{}
	for i := 0; i < 4; i++ {
		sender, err = NewNativeLogger(fmt.Sprintf("native-%d", i), l)
		s.Require().NoError(err)
		multiSenders = append(multiSenders, sender)
	}

	multi, err := NewMultiSender("multi", l, multiSenders)
	s.Require().NoError(err)
	s.senders["multi"] = multi

	s.tempDir, err = ioutil.TempDir("", "sender-test")
	s.Require().NoError(err)

	slackMocked, err := NewSlackLogger(&SlackOptions{
		client:   &slackClientMock{},
		Hostname: "testhost",
		Channel:  "#test",
		Name:     "smoke",
	}, "slack", LevelInfo{level.Info, level.Notice})
	s.Require().NoError(err)
	s.senders["slack-mocked"] = slackMocked

	xmppMocked, err := NewXMPPLogger("xmpp", "target",
		XMPPConnectionInfo{client: &xmppClientMock{}},
		LevelInfo{level.Info, level.Notice})
	s.Require().NoError(err)
	s.senders["xmpp-mocked"] = xmppMocked

	bufferedInternal, err := NewNativeLogger("buffered", l)
	s.Require().NoError(err)
	s.senders["buffered"] = NewBufferedSender(bufferedInternal, minBufferLength, 1)

	s.senders["github"], err = NewGithubIssuesLogger("gh", GithubOptions{})
	s.Require().NoError(err)

	s.senders["github-comment"], err = NewGithubCommentLogger("ghcomment", 100, GithubOptions{})
	s.Require().NoError(err)

	s.senders["gh-mocked"] = &githubLogger{
		Base: NewBase("gh-mocked"),
		gh:   &githubClientMock{},
	}
	s.senders["gh-mocked"].SetFormatter(MakeDefaultFormatter())
	s.senders["gh-comment-mocked"] = &githubCommentLogger{
		Base:  NewBase("gh-mocked"),
		gh:    &githubClientMock{},
		issue: 200,
	}
	s.senders["gh-comment-mocked"].SetFormatter(MakeDefaultFormatter())
}

func (s *SenderSuite) TeardownTest() {
	s.Require().NoError(os.RemoveAll(s.tempDir))
	for _, sender := range s.senders {
		s.NoError(sender.Close())
	}
}

func (s *SenderSuite) functionalMockSenders() map[string]Sender {
	out := map[string]Sender{}
	for t, sender := range s.senders {
		if t == "slack" || t == "internal" || t == "xmpp" || t == "buildlogger" {
			continue
		} else if strings.HasPrefix(t, "github") {
			continue

		} else {
			out[t] = sender
		}
	}
	return out
}

func (s *SenderSuite) TeardownSuite() {
	s.NoError(s.senders["internal"].Close())
}

func (s *SenderSuite) TestSenderImplementsInterface() {
	// this actually won't catch the error; the compiler will in
	// the fixtures, but either way we need to make sure that the
	// tests actually enforce this.
	for name, sender := range s.senders {
		s.Implements((*Sender)(nil), sender, name)
	}
}

const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890!@#$%^&*()"

func randomString(n int, r *rand.Rand) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[r.Int63()%int64(len(letters))]
	}
	return string(b)
}

func (s *SenderSuite) TestNameSetterRoundTrip() {
	for n, sender := range s.senders {
		for i := 0; i < 100; i++ {
			name := randomString(12, s.rand)
			s.NotEqual(sender.Name(), name, n)
			sender.SetName(name)
			s.Equal(sender.Name(), name, n)
		}
	}
}

func (s *SenderSuite) TestLevelSetterRejectsInvalidSettings() {
	levels := []LevelInfo{
		{level.Invalid, level.Invalid},
		{level.Priority(-10), level.Priority(-1)},
		{level.Debug, level.Priority(-1)},
		{level.Priority(800), level.Priority(-2)},
	}

	for n, sender := range s.senders {
		s.NoError(sender.SetLevel(LevelInfo{level.Debug, level.Alert}))
		for _, l := range levels {
			s.True(sender.Level().Valid(), string(n))
			s.False(l.Valid(), string(n))
			s.Error(sender.SetLevel(l), string(n))
			s.True(sender.Level().Valid(), string(n))
			s.NotEqual(sender.Level(), l, string(n))
		}

	}
}

func (s *SenderSuite) TestCloserShouldUsusallyNoop() {
	for t, sender := range s.senders {
		s.NoError(sender.Close(), string(t))
	}
}

func (s *SenderSuite) TestBasicNoopSendTest() {
	for _, sender := range s.functionalMockSenders() {
		for i := -10; i <= 110; i += 5 {
			m := message.NewDefaultMessage(level.Priority(i), "hello world! "+randomString(10, s.rand))
			sender.Send(m)
		}
	}
}

func TestBaseConstructor(t *testing.T) {
	assert := assert.New(t)

	sink, err := NewInternalLogger("sink", LevelInfo{level.Debug, level.Debug})
	assert.NoError(err)
	handler := ErrorHandlerFromSender(sink)
	assert.Equal(0, sink.Len())

	for outterIdx, n := range []string{"logger", "grip", "sender"} {
		made := MakeBase(n, func() {}, func() error { return nil })
		newed := NewBase(n)
		assert.Equal(made.name, newed.name)
		assert.Equal(made.level, newed.level)
		assert.Equal(made.closer(), newed.closer())

		assert.Equal(0, sink.Len())

		for innerIdx, s := range []*Base{made, newed} {
			assert.Error(s.SetFormatter(nil))
			assert.Error(s.SetErrorHandler(nil))
			assert.NoError(s.SetErrorHandler(handler))
			s.ErrorHandler(errors.New("failed"), message.NewString("fated"))
			assert.True(sink.HasMessage())

			assert.Equal(2, sink.Len(), "%d.%d", outterIdx, innerIdx)

			errMsg := sink.GetMessage()
			assert.Equal("failed", errMsg.Message.String())
			assert.Equal("failed", errMsg.Rendered)
			assert.Equal(level.Error, errMsg.Priority)
			assert.True(errMsg.Logged)

			msgMsg := sink.GetMessage()
			assert.Equal("fated", msgMsg.Message.String())
			assert.Equal("fated", msgMsg.Rendered)
			assert.Equal(level.Invalid, msgMsg.Priority)
			assert.False(msgMsg.Logged)

			assert.Equal(0, sink.Len())
			assert.False(sink.HasMessage())

			s.ErrorHandler(nil, message.NewString("really-fated"))
			assert.Equal(0, sink.Len())
			assert.False(sink.HasMessage())
		}
	}
}
