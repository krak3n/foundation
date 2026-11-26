package etcd

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.krak3n.dev/foundation/config"
)

// The limits that bound what the provider reads. A value or a prefix over them is
// an error, never cut short.
const (
	// MaxFields is the most fields (not counting groups) of one struct that are
	// read together, which is what etcd allows in one transaction by default.
	MaxFields = 128

	// MaxValueBytes is the longest value of one key.
	MaxValueBytes = 64 << 10

	// MaxTotalBytes is the most value text read for one struct.
	MaxTotalBytes = 1 << 20

	// MaxPrefixBytes is the longest prefix.
	MaxPrefixBytes = 256

	// DefaultTimeout is the time a read may take, unless WithTimeout says otherwise.
	DefaultTimeout = 5 * time.Second

	// MinTimeout and MaxTimeout are the bounds of WithTimeout.
	MinTimeout = 100 * time.Millisecond
	MaxTimeout = time.Minute

	// maxNameBytes is the longest name of a field in a key.
	maxNameBytes = 64

	// maxResponse is what the client may receive for one read: every value, and room
	// for the keys and the framing of each.
	maxResponse = MaxTotalBytes + MaxFields*1024
)

// An Option changes a [Provider].
type Option interface {
	apply(*Provider)
}

// WithTimeout sets how long one read of a struct may take, from MinTimeout to
// MaxTimeout; the default is DefaultTimeout. A value outside them is an error when
// the provider is used, never clamped.
func WithTimeout(d time.Duration) Option {
	return optionFunc(func(p *Provider) { p.timeout = d })
}

// A Provider reads configuration from etcd. It is safe for concurrent use and
// gives the same answer when asked again, for as long as the keys are the same.
type Provider struct {
	client  *clientv3.Client
	prefix  string
	timeout time.Duration
}

// New makes a provider that reads the keys under prefix with client. The prefix is
// required and is checked when the provider is used (as a path is by a file
// provider): it starts with a slash, is not only a slash, does not end in one, has
// no empty, "." or ".." segment, no control, format or space characters, and is
// valid UTF-8 of at most MaxPrefixBytes.
func New(client *clientv3.Client, prefix string, opts ...Option) *Provider {
	p := &Provider{client: client, prefix: prefix, timeout: DefaultTimeout}

	for _, opt := range opts {
		if opt != nil {
			opt.apply(p)
		}
	}

	return p
}

// RemoteLayer marks the provider as the remote layer.
func (*Provider) RemoteLayer() {}

// Describe lists the key of each field of dst for the function at path, without
// reading anything.
func (p *Provider) Describe(path []string, dst any) ([]config.FieldKey, error) {
	leaves, errs, err := p.plan(path, dst)
	if err != nil {
		return nil, err
	}

	if len(leaves) > MaxFields {
		return nil, errors.Join(append(errs, invalid("too many fields to read at once"))...)
	}

	keys := make([]config.FieldKey, len(leaves))
	for i, l := range leaves {
		keys[i] = config.FieldKey{Field: l.field, Key: l.key}
	}

	keys, err = config.Annotate("etcd", dst, keys)

	return keys, errors.Join(append(errs, err)...)
}

// Unmarshal reads the keys of the fields of dst for the function at path in one
// transaction. A key that is not there leaves its field alone. If the cluster
// can't be read, or answers in a way that no honest one would, nothing is set.
func (p *Provider) Unmarshal(path []string, dst any) error {
	leaves, errs, err := p.plan(path, dst)
	if err != nil {
		return err
	}

	if len(leaves) > MaxFields {
		return errors.Join(append(errs, invalid("too many fields to read at once"))...)
	}

	if len(leaves) == 0 {
		return errors.Join(errs...)
	}

	values, err := p.read(leaves)
	if err != nil {
		return err
	}

	for i, l := range leaves {
		v := values[i]
		if v == nil {
			continue
		}

		fe := func(reason error) error {
			return &config.FieldError{Provider: "etcd", Field: l.field, Key: l.key, Err: reason}
		}

		switch {
		case len(*v) > MaxValueBytes:
			errs = append(errs, fe(errors.New("value too large")))
		case !utf8.ValidString(*v) || strings.ContainsRune(*v, 0):
			errs = append(errs, fe(errors.New("value is not valid text")))
		case *v == "" && !config.IsPlainString(l.value.Type()):
			// An empty value is not set, so that a stray empty key doesn't wipe a
			// default out or fail to parse.
		default:
			if err := config.SetString(l.value, *v); err != nil {
				errs = append(errs, fe(err))
			}
		}
	}

	return errors.Join(errs...)
}

// check says what is wrong with the prefix and the timeout, if anything.
func (p *Provider) check() error {
	if p.timeout < MinTimeout || p.timeout > MaxTimeout {
		return invalid("invalid timeout: it must be between " + MinTimeout.String() + " and " + MaxTimeout.String())
	}

	return checkPrefix(p.prefix)
}

// plan checks the provider and dst and lists what to read.
func (p *Provider) plan(path []string, dst any) ([]leaf, []error, error) {
	elem, err := structOf(dst)
	if err != nil {
		return nil, nil, err
	}

	if err := p.check(); err != nil {
		return nil, nil, err
	}

	dir := p.prefix

	for _, k := range config.Keys(path) {
		dir += "/" + strings.ToLower(k)
	}

	var leaves []leaf

	errs := walk(elem, dir, "", map[string]string{}, &leaves)

	return leaves, errs, nil
}

// read asks for every key in one transaction and checks the answer, returning a
// value for each key that is there, in the order asked. An answer that is not
// exactly what was asked is an error, whatever else it holds.
func (p *Provider) read(leaves []leaf) (values []*string, err error) {
	// A client that was not made by clientv3.New has no connection, and the call
	// panics: that is a failure to read, not a crash at startup.
	defer func() {
		if recover() != nil {
			values, err = nil, &readError{reason: "request failed"}
		}
	}()

	if p.client == nil {
		return nil, invalid("no client")
	}

	req := &etcdserverpb.TxnRequest{}

	for _, l := range leaves {
		req.Success = append(req.Success, &etcdserverpb.RequestOp{
			Request: &etcdserverpb.RequestOp_RequestRange{RequestRange: &etcdserverpb.RangeRequest{Key: []byte(l.key), Limit: 1}},
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
	defer cancel()

	resp, err := clientv3.RetryKVClient(p.client).Txn(ctx, req, grpc.MaxCallRecvMsgSize(maxResponse), grpc.WaitForReady(true))
	if err != nil {
		return nil, failure(ctx, err)
	}

	wrong := &readError{reason: "the answer is not what was asked for"}

	if resp == nil || !resp.Succeeded || len(resp.Responses) != len(leaves) {
		return nil, wrong
	}

	var total int

	values = make([]*string, len(leaves))

	for i, l := range leaves {
		rr := resp.Responses[i].GetResponseRange()
		if rr == nil || len(rr.Kvs) > 1 {
			return nil, wrong
		}

		if len(rr.Kvs) == 0 {
			continue
		}

		kv := rr.Kvs[0]
		if kv == nil || string(kv.Key) != l.key {
			return nil, wrong
		}

		if total += len(kv.Value); total > MaxTotalBytes {
			return nil, &readError{reason: "answer too large"}
		}

		v := string(kv.Value)
		values[i] = &v
	}

	return values, nil
}

type optionFunc func(*Provider)

func (f optionFunc) apply(p *Provider) { f(p) }

// A readError is a failure to read, with a fixed reason. The error of the client
// is not kept: it can name an address, a key or a user.
type readError struct {
	reason string
	cause  error
}

func (e *readError) Error() string { return "etcd: could not read configuration: " + e.reason }

func (e *readError) Unwrap() error { return e.cause }

// failure turns what a call returned into a reason. The deadline is the only cause
// that is kept, for errors.Is.
func failure(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return &readError{reason: "timed out", cause: context.DeadlineExceeded}
	}

	switch status.Code(err) {
	case codes.DeadlineExceeded:
		return &readError{reason: "timed out", cause: context.DeadlineExceeded}
	case codes.PermissionDenied:
		return &readError{reason: "permission denied"}
	case codes.Unauthenticated:
		return &readError{reason: "not authenticated"}
	case codes.Unavailable:
		return &readError{reason: "unavailable"}
	case codes.ResourceExhausted:
		return &readError{reason: "a limit was reached, such as the size of the answer or too many requests"}
	}

	return &readError{reason: "request failed"}
}

// invalid is the error for a mistake in the provider's own settings, without the
// setting.
func invalid(reason string) error { return errors.New("etcd: " + reason) }

func checkPrefix(prefix string) error {
	reason := ""

	switch {
	case prefix == "":
		reason = "it is required"
	case len(prefix) > MaxPrefixBytes:
		reason = "it is too long"
	case !utf8.ValidString(prefix):
		reason = "it is not valid UTF-8"
	case prefix[0] != '/':
		reason = "it must start with a slash"
	case prefix == "/":
		reason = "it must name something under the root"
	case strings.HasSuffix(prefix, "/"):
		reason = "it must not end with a slash"
	}

	if reason == "" {
		for _, seg := range strings.Split(prefix[1:], "/") {
			if seg == "" || seg == "." || seg == ".." {
				reason = "it must have no empty, . or .. segment"

				break
			}
		}
	}

	if reason == "" {
		for _, r := range prefix {
			if unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zs, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
				reason = "it must have no control, format or space characters"

				break
			}
		}
	}

	if reason != "" {
		return invalid("invalid prefix: " + reason)
	}

	return nil
}

// A leaf is a field to read, and the key it is read from.
type leaf struct {
	field string
	key   string
	value reflect.Value
}

// structOf returns the struct dst points to.
func structOf(dst any) (reflect.Value, error) {
	rv := reflect.ValueOf(dst)

	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return reflect.Value{}, invalid("the destination must be a non-nil pointer to a struct")
	}

	return rv.Elem(), nil
}

// parseTag reads an etcd tag: a name, which is a part of a key and so a plain
// word, and no options. "-" skips the field, and no name takes the common name.
func parseTag(tag string) (config.Tag, error) {
	name, opts, _ := strings.Cut(tag, ",")

	switch {
	case opts != "":
		return config.Tag{}, errors.New("unknown option in the etcd tag")
	case name == "-":
		return config.Tag{Skip: true}, nil
	case name == "":
		return config.Tag{}, nil
	case !plainWord(name):
		return config.Tag{}, errors.New("the etcd tag's name must be a letter then letters, digits and underscores, at most 64 bytes")
	}

	return config.Tag{Name: name}, nil
}

// plainWord is [A-Za-z][A-Za-z0-9_]* of at most maxNameBytes.
func plainWord(s string) bool {
	if s == "" || len(s) > maxNameBytes {
		return false
	}

	for i := range len(s) {
		c := s[i]

		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '_'):
		default:
			return false
		}
	}

	return true
}

// tagMistake reports an etcd key that is given more than once (reflect would use
// the first and drop the rest) or whose value can't be read. It follows how
// reflect.StructTag reads a tag, so what the other keys of the tag hold, even the
// text "etcd:", is no mistake.
func tagMistake(tag reflect.StructTag) bool {
	seen := 0

	for tag != "" {
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}

		if tag = tag[i:]; tag == "" {
			break
		}

		i = 0
		for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' && tag[i] != 0x7f {
			i++
		}

		if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
			return strings.HasPrefix(string(tag), "etcd:")
		}

		name := string(tag[:i])
		tag = tag[i+1:]

		i = 1
		for i < len(tag) && tag[i] != '"' {
			if tag[i] == '\\' {
				i++
			}

			i++
		}

		if i >= len(tag) {
			return name == "etcd"
		}

		quoted := string(tag[:i+1])
		tag = tag[i+1:]

		if name == "etcd" {
			if _, err := strconv.Unquote(quoted); err != nil {
				return true
			}

			seen++
		}
	}

	return seen > 1
}

// walk lists the fields of rv to read, with their keys, and the problems with the
// struct. dir is the key of the struct, goPrefix the Go path to it.
func walk(rv reflect.Value, dir, goPrefix string, claimed map[string]string, leaves *[]leaf) []error {
	fields, problems := config.Fields(rv, "etcd", parseTag)

	var errs []error

	for i := range rv.NumField() {
		if sf := rv.Type().Field(i); tagMistake(sf.Tag) {
			errs = append(errs, &config.FieldError{Provider: "etcd", Field: goPath(goPrefix, sf.Name), Err: errors.New("the etcd tag is given twice or can't be read")})
		}
	}

	for _, pr := range problems {
		key := ""
		if plainWord(pr.Name) {
			key = dir + "/" + pr.Name
		}

		errs = append(errs, &config.FieldError{Provider: "etcd", Field: goPath(goPrefix, pr.GoName), Key: key, Err: pr.Err})
	}

	for _, f := range fields {
		field := goPath(goPrefix, f.GoName)
		key := dir + "/" + f.Name

		if f.Group {
			errs = append(errs, walk(f.Value, key, field, claimed, leaves)...)

			continue
		}

		if other, ok := claimed[key]; ok {
			errs = append(errs, &config.FieldError{
				Provider: "etcd", Field: field, Key: key,
				Err: fmt.Errorf("key is also read by field %s; rename one of them", other),
			})

			continue
		}

		claimed[key] = field
		*leaves = append(*leaves, leaf{field: field, key: key, value: f.Value})
	}

	return errs
}

func goPath(prefix, name string) string {
	if prefix == "" {
		return name
	}

	return prefix + "." + name
}
