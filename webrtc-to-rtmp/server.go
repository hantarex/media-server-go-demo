package main

import "C"

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
	mediaserver "github.com/notedit/media-server-go"
	rtmppusher "github.com/notedit/media-server-go-demo/webrtc-to-rtmp/rtmp"
	"github.com/notedit/sdp"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type Message struct {
	SdpAnswer string `json:"sdpAnswer,omitempty"`
	SdpOffer  string `json:"sdpOffer,omitempty"`
	Id        string `json:"id,omitempty"`
	Key       string `json:"key,omitempty"`
}

const RtmpServer = "rtmp://127.0.0.1/live/"

var upGrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var Capabilities = map[string]*sdp.Capability{
	"audio": &sdp.Capability{
		Codecs: []string{"opus"},
	},
	"video": &sdp.Capability{
		Codecs: []string{"h264"},
		Rtx:    true,
		Rtcpfbs: []*sdp.RtcpFeedback{
			&sdp.RtcpFeedback{
				ID: "goog-remb",
			},
			&sdp.RtcpFeedback{
				ID: "transport-cc",
			},
			&sdp.RtcpFeedback{
				ID:     "ccm",
				Params: []string{"fir"},
			},
			&sdp.RtcpFeedback{
				ID:     "nack",
				Params: []string{"pli"},
			},
		},
		Extensions: []string{
			"urn:3gpp:video-orientation",
			"http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01",
			"http://www.webrtc.org/experiments/rtp-hdrext/abs-send-time",
			"urn:ietf:params:rtp-hdrext:toffse",
			"urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id",
			"urn:ietf:params:rtp-hdrext:sdes:mid",
		},
	},
}

func channel(c *gin.Context) {
	var forceQuit bool
	sig := c.MustGet("signal").(chan os.Signal)
	ws, err := upGrader.Upgrade(c.Writer, c.Request, nil)
	ticker := time.NewTicker(time.Second * 30)
	defer ticker.Stop()
	if err != nil {
		return
	}
	defer ws.Close()

	var transport *mediaserver.Transport
	endpoint := mediaserver.NewEndpoint("127.0.0.1")
	defer endpoint.Stop()
	defer fmt.Println("Stop!!!")

	go func() {
		for {
			msg := <-sig
			if msg == syscall.SIGPIPE {
				forceQuit = true
				ws.Close()
			}
		}
	}()

	go func() {
		for {
			<-ticker.C
			if err := ws.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
				ws.Close()
			}
		}
	}()

	for {
		// read json
		var msg Message
		err = ws.ReadJSON(&msg)
		if err != nil {
			fmt.Println("error: ", err)
			break
		}

		if msg.Id == "start" {
			offer, err := sdp.Parse(msg.SdpOffer)
			if err != nil {
				panic(err)
			}
			transport = endpoint.CreateTransport(offer, nil)
			transport.SetRemoteProperties(offer.GetMedia("audio"), offer.GetMedia("video"))

			answer := offer.Answer(transport.GetLocalICEInfo(),
				transport.GetLocalDTLSInfo(),
				endpoint.GetLocalCandidates(),
				Capabilities)

			transport.SetLocalProperties(answer.GetMedia("audio"), answer.GetMedia("video"))
			transport.SetBandwidthProbing(true)
			transport.SetMaxProbingBitrate(5000)

			for _, stream := range offer.GetStreams() {
				incomingStream := transport.CreateIncomingStream(stream)
				defer incomingStream.Stop()

				// Хуй знает зачем. Кипалив чтоли?
				//refresher := mediaserver.NewRefresher(5000)
				//refresher.AddStream(incomingStream)

				outgoingStream := transport.CreateOutgoingStream(stream.Clone())
				outgoingStream.AttachTo(incomingStream)
				answer.AddStream(outgoingStream.GetStreamInfo())

				pusher, err := rtmppusher.NewRtmpPusher(RtmpServer + msg.Key)
				defer pusher.Stop()
				if err != nil {
					panic(err)
				}

				pusher.Start()
				if len(incomingStream.GetVideoTracks()) > 0 {

					videoTrack := incomingStream.GetVideoTracks()[0]

					videoTrack.OnMediaFrame(func(frame []byte, timestamp uint64) {

						fmt.Println("video frame ==========")
						if len(frame) <= 4 {
							return
						}

						pusher.Push(frame, false)
					})
				}

				if len(incomingStream.GetAudioTracks()) > 0 {

					audioTrack := incomingStream.GetAudioTracks()[0]

					audioTrack.OnMediaFrame(func(frame []byte, timestamp uint64) {

						fmt.Println("audio frame ===== ")
						if len(frame) <= 4 {
							return
						}

						pusher.Push(frame, true)
					})
				}

			}

			ws.WriteJSON(Message{
				Id:        "startResponse",
				SdpAnswer: answer.String(),
			})
		}
	}
}

func index(c *gin.Context) {
	fmt.Println("helloworld")
	c.HTML(http.StatusOK, "index.html", gin.H{})
}

func SetupCloseHandler(c chan os.Signal) {
	//signal.Notify(c, os.Interrupt, syscall.SIGINT, syscall.SIGPIPE)
	signal.Notify(c, syscall.SIGPIPE)
	//go func() {
	//	for {
	//		select {
	//			case msg := <-c:
	//				if msg == syscall.SIGINT {
	//					fmt.Println("\r- Ctrl+C pressed in Terminal")
	//					//os.Exit(0)
	//				}
	//				if msg == syscall.SIGPIPE {
	//					fmt.Println("\r- Pipe closed")
	//				}
	//		}
	//	}
	//}()
}

func main() {
	c := make(chan os.Signal, 1)
	fmt.Println(c)
	SetupCloseHandler(c)
	godotenv.Load()
	mediaserver.EnableDebug(true)
	mediaserver.EnableLog(true)
	address := ":8443"
	if os.Getenv("port") != "" {
		address = ":" + os.Getenv("port")
	}

	r := gin.Default()
	r.Use(func(context *gin.Context) {
		context.Set("signal", c)
		context.Next()
	})
	//r.LoadHTMLFiles("./index.html")
	r.GET("/magicmirror", channel)
	//r.GET("/", index)
	r.Run(address)
}
