package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/smtp"
	"os"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/mail"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Render oýa saklamak üçin HTTP endpoint
	go func() {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "Mail Bot (Test Mode) işläp dur!")
		})
		log.Printf("HTTP serwer %s portda işe düşdi...", port)
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			log.Fatalf("HTTP serwer ýalňyşlygy: %v", err)
		}
	}()

	// Her 30 sekuntdan mail barlamak
	for {
		log.Println("Täze hatlar barlanylýar...")
		checkAndReplyMails()
		time.Sleep(30 * time.Second)
	}
}

func checkAndReplyMails() {
	imapServer := os.Getenv("IMAP_SERVER") // mes: imap.gmail.com:993
	smtpServer := os.Getenv("SMTP_SERVER") // mes: smtp.gmail.com:587
	email := os.Getenv("EMAIL_USER")
	password := os.Getenv("EMAIL_PASS")

	if imapServer == "" || email == "" || password == "" {
		log.Println("Ýalňyşlyk: Environment Variable maglumatlary doly däl!")
		return
	}

	smtpHost := strings.Split(smtpServer, ":")[0]

	// 1. IMAP Baglanşyk
	c, err := client.DialTLS(imapServer, nil)
	if err != nil {
		log.Printf("IMAP baglanşyk ýalňyşlygy: %v", err)
		return
	}
	defer c.Logout()

	if err := c.Login(email, password); err != nil {
		log.Printf("IMAP Giriş ýalňyşlygy: %v", err)
		return
	}

	mbox, err := c.Select("INBOX", false)
	if err != nil || mbox.Messages == 0 {
		return
	}

	// Unseen (Okalmadyk) hatlary gözlemek
	criteria := imap.NewSearchCriteria()
	criteria.WithoutFlags = []string{imap.SeenFlag}
	seqNums, err := c.Search(criteria)
	if err != nil || len(seqNums) == 0 {
		return
	}

	log.Printf("%d sany täze okalmadyk hat tapyldy.", len(seqNums))

	seqset := new(imap.SeqSet)
	seqset.AddNum(seqNums...)

	section := &imap.BodySectionName{}
	items := []imap.FetchItem{section.FetchItem(), imap.FetchEnvelope}
	messages := make(chan *imap.Message, 10)

	done := make(chan error, 1)
	go func() {
		done <- c.Fetch(seqset, items, messages)
	}()

	for msg := range messages {
		if msg == nil {
			continue
		}

		r := msg.GetBody(section)
		if r == nil {
			continue
		}

		mr, err := mail.CreateReader(r)
		if err != nil {
			continue
		}

		header := mr.Header
		subject, _ := header.Subject()
		from, _ := header.AddressList("From")

		if len(from) == 0 {
			continue
		}

		senderEmail := from[0].Address
		messageID, _ := header.Text("Message-ID")

		// 1. DIŇE "SMS alert:" BİLEN BAŞLAÝAN HATLARY SÜZMEK
		if !strings.HasPrefix(subject, "SMS alert:") {
			log.Printf("SMS Alert däl hat skipped edildi: Subject='%s'", subject)
			continue
		}

		// 2. Self-Loop barlagy (Öz-özüne hat ugratmazlyk üçin)
		if strings.EqualFold(senderEmail, email) {
			log.Println("Öz ugradan hatyňyz skipped edildi (Loop önüni almaq üçin).")
			markAsSeen(c, msg.SeqNum)
			continue
		}

		// 3. Body text-ini okamak
		var bodyText string
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}

			switch p.Header.(type) {
			case *mail.InlineHeader:
				b, _ := io.ReadAll(p.Body)
				bodyText = string(b)
			}
		}

		// 4. "Sms Tekst:" DİÝEN ÝERDEN SOŇKY HAKYKY PROMPT-Y AJYRATMAK
		actualPrompt := extractSmsContent(bodyText)
		if actualPrompt == "" {
			actualPrompt = bodyText
		}

		log.Printf("SMS Barlanyldy! Gelen Tekst: %s", actualPrompt)

		// 5. TEST STATİK JOGAP (Gemini API-syz)
		staticTestReply := fmt.Sprintf("TEST JOGAP: Soragyňyz altyldy!\n\nGelen SMS Teksti: %s\n\nStatus: Auto-Reply Bot dogry işleýär.", actualPrompt)

		// 6. SMTP Reply Ugratmak
		replySubject := "Re: " + subject
		err = sendSMTPReply(smtpHost, smtpServer, email, password, senderEmail, replySubject, staticTestReply, messageID)
		if err != nil {
			log.Printf("SMTP ugratmakda ýalňyşlyk: %v", err)
			continue
		}

		log.Printf("Statik jogap SMTP arkaly ugratyldy: %s", senderEmail)

		// 7. Haty Okaldy (\Seen) diýip bellemek
		markAsSeen(c, msg.SeqNum)
	}

	if err := <-done; err != nil {
		log.Printf("Fetch ýalňyşlygy: %v", err)
	}
}

// "Sms Tekst:" diýen sözden soňky bölegi kesip alýan kömekçi funksiýa
func extractSmsContent(body string) string {
	marker := "Sms Tekst:"
	idx := strings.Index(body, marker)
	if idx != -1 {
		return strings.TrimSpace(body[idx+len(marker):])
	}
	return strings.TrimSpace(body)
}

func markAsSeen(c *client.Client, seqNum uint32) {
	item := imap.FormatFlagsOp(imap.AddFlags, true)
	flags := []interface{}{imap.SeenFlag}
	itemSeqSet := new(imap.SeqSet)
	itemSeqSet.AddNum(seqNum)
	c.Store(itemSeqSet, item, flags, nil)
}

func sendSMTPReply(host, addr, from, password, to, subject, body, inReplyTo string) error {
	auth := smtp.PlainAuth("", from, password, host)

	msg := fmt.Sprintf("From: %s\r\n"+
		"To: %s\r\n"+
		"Subject: %s\r\n"+
		"In-Reply-To: %s\r\n"+
		"References: %s\r\n"+
		"Content-Type: text/plain; charset=UTF-8\r\n"+
		"\r\n"+
		"%s\r\n", from, to, subject, inReplyTo, inReplyTo, body)

	c, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer c.Close()

	config := &tls.Config{ServerName: host}
	if err = c.StartTLS(config); err != nil {
		return err
	}

	if err = c.Auth(auth); err != nil {
		return err
	}

	if err = c.Mail(from); err != nil {
		return err
	}

	if err = c.Rcpt(to); err != nil {
		return err
	}

	w, err := c.Data()
	if err != nil {
		return err
	}

	_, err = w.Write([]byte(msg))
	if err != nil {
		return err
	}

	err = w.Close()
	if err != nil {
		return err
	}

	return c.Quit()
}