package up

import (
	"context"
	"github.com/expr-lang/expr/vm"
	"github.com/gotd/td/telegram/peers"
	"github.com/iyear/tdl/pkg/texpr"
	"github.com/mitchellh/mapstructure"
	"os"

	"github.com/gabriel-vasile/mimetype"
	"github.com/go-faster/errors"
	"github.com/iyear/tdl/pkg/uploader"
	"github.com/iyear/tdl/pkg/utils"
)

type toEnv struct {
	File File
}

func exprToEnv(file *File) toEnv {
	if file == nil {
		file = &File{}
	}
	return toEnv{File: *file}
}

type File struct {
	File  string `comment:"File path"`
	Thumb string `comment:"Thumbnail path"`
}

type iter struct {
	files   []*File
	to      *vm.Program
	chat    string
	topic   int
	photo   bool
	remove  bool
	manager *peers.Manager

	cur  int
	err  error
	file uploader.Elem
}

type dest struct {
	Peer   string
	Thread int
}

func newIter(files []*File, to *vm.Program, chat string, topic int, photo, remove bool, manager *peers.Manager) *iter {
	return &iter{
		files:   files,
		to:      to,
		chat:    chat,
		topic:   topic,
		photo:   photo,
		remove:  remove,
		manager: manager,

		cur:  0,
		err:  nil,
		file: nil,
	}
}

func (i *iter) Next(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		i.err = ctx.Err()
		return false
	default:
	}

	if i.cur >= len(i.files) || i.err != nil {
		return false
	}

	cur := i.files[i.cur]
	i.cur++

	f, err := os.Open(cur.File)
	if err != nil {
		i.err = errors.Wrap(err, "open file")
		return false
	}

	var (
		to     peers.Peer
		thread int
	)
	if i.chat != "" {
		to, i.err = i.resolvePeer(ctx, i.chat)
		thread = i.topic
		if i.err != nil {
			return false
		}
	} else {
		// message routing
		result, err := texpr.Run(i.to, exprToEnv(cur))
		if err != nil {
			i.err = errors.Wrap(err, "message routing")
			return false
		}

		switch r := result.(type) {
		case string:
			// pure chat, no reply to, which is a compatible with old version
			// and a convenient way to send message to self
			to, err = i.resolvePeer(ctx, r)
		case map[string]interface{}:
			// chat with reply to topic or message
			var d dest

			if err = mapstructure.WeakDecode(r, &d); err != nil {
				i.err = errors.Wrapf(err, "decode dest: %v", result)
				return false
			}

			to, err = i.resolvePeer(ctx, d.Peer)
			thread = d.Thread
		default:
			i.err = errors.Errorf("message router must return string or dest: %T", result)
			return false
		}
	}

	var thumb *uploaderFile = nil
	// has thumbnail
	if cur.Thumb != "" {
		tMime, err := mimetype.DetectFile(cur.Thumb)
		if err != nil || !utils.Media.IsImage(tMime.String()) { // TODO(iyear): jpg only
			i.err = errors.Wrapf(err, "invalid thumbnail file: %v", cur.Thumb)
			return false
		}
		thumbFile, err := os.Open(cur.Thumb)
		if err != nil {
			i.err = errors.Wrap(err, "open thumbnail file")
			return false
		}

		thumb = &uploaderFile{File: thumbFile, size: 0}
	}

	stat, err := f.Stat()
	if err != nil {
		i.err = errors.Wrap(err, "stat file")
		return false
	}

	i.file = &iterElem{
		file:    &uploaderFile{File: f, size: stat.Size()},
		thumb:   thumb,
		to:      to,
		thread:  thread,
		asPhoto: i.photo,
		remove:  i.remove,
	}

	return true
}

func (i *iter) resolvePeer(ctx context.Context, peer string) (peers.Peer, error) {
	if peer == "" { // self
		return i.manager.Self(ctx)
	}

	return utils.Telegram.GetInputPeer(ctx, i.manager, peer)
}

func (i *iter) Value() uploader.Elem {
	return i.file
}

func (i *iter) Err() error {
	return i.err
}
