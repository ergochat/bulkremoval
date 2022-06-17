package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"regexp"
	"regexp/syntax"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ergochat/irc-go/ircevent"
	"github.com/ergochat/irc-go/ircmsg"
)

type empty struct{}

/* begin glob code copied from ergo */
func CompileGlob(glob string) (result *regexp.Regexp, err error) {
	var buf strings.Builder
	buf.WriteByte('^')
	err = addRegexp(&buf, glob)
	if err != nil {
		return
	}
	buf.WriteByte('$')
	return regexp.Compile(buf.String())
}

func addRegexp(buf *strings.Builder, glob string) (err error) {
	for _, r := range glob {
		switch r {
		case '*':
			buf.WriteString(".*")
		case '?':
			buf.WriteString(".")
		case 0xFFFD:
			return &syntax.Error{Code: syntax.ErrInvalidUTF8, Expr: glob}
		default:
			buf.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return
}
/* end glob code copied from ergo */

type BulkRemovalClient struct {
	Connection ircevent.Connection

	channel  string
	operName string
	operPass string
	nuhGlob  *regexp.Regexp

	chathistoryLimit int

	startSelector   string
	selectorIsMsgid bool
	msgError        error

	joinComplete chan empty
}

// Get the next batch of messages in forward pagination order, without any filtering.
// The pagination position is stored internally in the *BulkRemovalClient.
func (b *BulkRemovalClient) GetMessages() (messages []ircmsg.Message, err error) {
	var selector string
	if b.selectorIsMsgid {
		selector = fmt.Sprintf("msgid=%s", b.startSelector)
	} else {
		selector = fmt.Sprintf("timestamp=%s", b.startSelector)
	}

	batch, err := b.Connection.GetLabeledResponse(nil, "CHATHISTORY", "AFTER", b.channel, selector, strconv.Itoa(b.chathistoryLimit))
	if err != nil {
		return
	}

	messages = make([]ircmsg.Message, 0, len(batch.Items))
	switch batch.Command {
	case "BATCH":
		FlattenBatch(batch, &messages)
	default:
		// probably a FAIL of some kind
		line, _ := batch.Line()
		err = fmt.Errorf("Unexpected response from CHATHISTORY: %s", line)
	}
	if len(messages) != 0 {
		_, b.startSelector = messages[len(messages)-1].GetTag("msgid")
		b.selectorIsMsgid = true
	}
	return
}

// helper to recursively flatten a BATCH of messages
func FlattenBatch(batch *ircevent.Batch, results *[]ircmsg.Message) {
	if batch.Command == "BATCH" {
		for _, msg := range batch.Items {
			FlattenBatch(msg, results)
		}
	} else {
		if batch.HasTag("msgid") {
			*results = append(*results, batch.Message)
		}
	}
}

func (b *BulkRemovalClient) DeleteMsgids(msgids []string) {
	for _, msgid := range msgids {
		b.Connection.Send("HISTSERV", "DELETE", b.channel, msgid)
	}
	b.Connection.GetLabeledResponse(nil, "PING", "synchronize")
}

func (b *BulkRemovalClient) connectCallback(msg ircmsg.Message) {
	isupport := b.Connection.ISupport()
	token := isupport["CHATHISTORY"]
	if token == "" {
		token = isupport["draft/CHATHISTORY"]
	}
	var err error
	b.chathistoryLimit, err = strconv.Atoi(token)
	if err != nil {
		log.Fatalf("chathistory limit not advertised: cannot proceed: `%s`", token)
	}

	b.Connection.Send("OPER", b.operName, b.operPass)
	b.Connection.Send("SAJOIN", b.channel)
}

func (b *BulkRemovalClient) fatalCallback(msg ircmsg.Message) {
	switch msg.Command {
	case ircevent.ERR_PASSWDMISMATCH:
		log.Fatalf("Your operator credentials were rejected: cannot proceed")
	case ircevent.ERR_NOPRIVILEGES:
		log.Fatalf("Your operator credentials are insufficient for SAJOIN: cannot proceed")
	default:
		log.Fatalf("Fatal error numeric: %s", msg.Command)
	}
}

func (b *BulkRemovalClient) joinCallback(msg ircmsg.Message) {
	channel := msg.Params[0]
	// this is why we need to switch Ergo's default casefolding to ascii:
	if channel == b.channel || strings.ToLower(channel) == strings.ToLower(b.channel) {
		close(b.joinComplete)
	}
}

func (b *BulkRemovalClient) WaitForJoin() {
	<-b.joinComplete
}

type summaryItem struct {
	Source   string
	Messages []ircmsg.Message
}

func (s *summaryItem) String() string {
	var exampleMsg string
	var exampleTime string
	for _, msg := range s.Messages {
		if msg.Command == "PRIVMSG" || msg.Command == "NOTICE" {
			exampleMsg = msg.Params[1]
			_, exampleTime = msg.GetTag("time")
			break
		}
	}
	return fmt.Sprintf("%d messages from %s : example : <%s> %s", len(s.Messages), s.Source, exampleTime, exampleMsg)
}

func (b *BulkRemovalClient) MatchAndSummarize(messages []ircmsg.Message) (msgids []string, summaryStrings []string) {
	msgsBySource := make(map[string][]ircmsg.Message)
	for _, message := range messages {
		if b.nuhGlob.MatchString(message.Source) {
			if _, msgid := message.GetTag("msgid"); msgid != "" {
				msgids = append(msgids, msgid)
				current := msgsBySource[message.Source]
				msgsBySource[message.Source] = append(current, message)
			}
		}
	}
	if len(msgids) == 0 {
		return
	}
	summaries := make([]summaryItem, 0, len(msgsBySource))
	for source, messages := range msgsBySource {
		summaries = append(summaries, summaryItem{source, messages})
	}
	sort.Slice(summaries, func(i, j int) bool {
		return len(summaries[i].Messages) > len(summaries[j].Messages)
	})
	summaryStrings = make([]string, 0, len(summaries))
	for _, summary := range summaries {
		summaryStrings = append(summaryStrings, summary.String())
	}
	return
}

func main() {

	nick := "bulkremoval"
	server := os.Getenv("IRCEVENT_SERVER")
	channel := os.Getenv("IRCEVENT_CHANNEL")
	saslLogin := os.Getenv("IRCEVENT_SASL_LOGIN")
	saslPassword := os.Getenv("IRCEVENT_SASL_PASSWORD")
	useTLS := os.Getenv("IRCEVENT_USE_PLAINTEXT") == ""
	operName := os.Getenv("IRCEVENT_OPER_NAME")
	operPass := os.Getenv("IRCEVENT_OPER_PASSWORD")

	glob, err := CompileGlob(os.Getenv("IRCEVENT_GLOB"))
	if err != nil {
		panic(err)
	}

	client := BulkRemovalClient{
		operName:     operName,
		operPass:     operPass,
		channel:      channel,
		nuhGlob:      glob,
		joinComplete: make(chan empty),

		startSelector: os.Getenv("IRCEVENT_START_TIME"),
	}

	client.Connection = ircevent.Connection{
		Server:       server,
		Nick:         nick,
		Debug:        true,
		UseTLS:       useTLS,
		RequestCaps:  []string{"server-time", "message-tags", "batch", "labeled-response", "chathistory", "draft/chathistory", "chathistory-events", "draft/event-playback"},
		SASLLogin:    saslLogin, // SASL will be enabled automatically if these are set
		SASLPassword: saslPassword,
	}

	client.Connection.AddConnectCallback(client.connectCallback)
	client.Connection.AddCallback(ircevent.ERR_PASSWDMISMATCH, client.fatalCallback)
	client.Connection.AddCallback(ircevent.ERR_NOPRIVILEGES, client.fatalCallback)
	client.Connection.AddCallback("JOIN", client.joinCallback)

	log.Printf("Waiting for successful channel join...\n")

	err = client.Connection.Connect()
	if err != nil {
		log.Fatal(err)
	}
	go client.Connection.Loop()

	defer client.Connection.Quit()

	client.WaitForJoin()
	log.Printf("Channel join complete.\n")

	// this loop is tricky. we cannot delete until we've completed the next fetch,
	// lest we delete the msgid that we're using as the pagination selector.
	// the sequence is: fetch, ask, (fetch, delete, ask)*,
	// where the delete applies to the IDs from the second-to-last fetch:
	scanner := bufio.NewScanner(os.Stdin)
	var deletableMsgids []string
	for {
		time.Sleep(500 * time.Millisecond) // fakelag is off, be a little cautious
		messages, err := client.GetMessages()
		if err != nil {
			log.Fatalf("error from CHATHISTORY: %v", err)
		}

		if len(deletableMsgids) != 0 {
			client.DeleteMsgids(deletableMsgids)
			deletableMsgids = nil
		}

		log.Printf("got %d messages\n", len(messages))
		if len(messages) == 0 {
			return // end of pagination sequence
		}
		msgids, summaryStrings := client.MatchAndSummarize(messages)
		log.Printf("got %d messages matching glob", len(msgids))
		if len(msgids) == 0 {
			continue
		}
		for _, summaryString := range summaryStrings {
			log.Println(summaryString)
		}

		log.Printf("Delete matching messages? y/n/q\n")

		if !scanner.Scan() {
			return
		}
		switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
		case "":
			return
		case "y", "yes":
			deletableMsgids = msgids
		case "q", "quit":
			return
		default:
			log.Printf("Skipping msgids and continuing")
		}
	}
}
