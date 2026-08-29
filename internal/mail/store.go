package mail

import (
	"strings"
	"sync"
)

// DefaultCapacity, saklanan en fazla posta sayısı.
//
// Yakalayıcı geliştirme aracı; sınırsız biriktirmek uzun oturumlarda belleği
// yiyor. En eskiler düşüyor.
const DefaultCapacity = 500

// Store, yakalanan postaları tutar ve yeni gelenleri abonelere duyurur.
type Store struct {
	mu       sync.RWMutex
	messages []*Message
	byID     map[string]*Message
	capacity int

	subs   map[int]chan *Message
	nextID int
}

// NewStore, verilen kapasiteyle bir depo oluşturur. 0 ise DefaultCapacity.
func NewStore(capacity int) *Store {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Store{
		byID:     make(map[string]*Message),
		capacity: capacity,
		subs:     make(map[int]chan *Message),
	}
}

// Add, postayı depoya ekler ve abonelere duyurur.
func (s *Store) Add(msg *Message) {
	s.mu.Lock()
	s.messages = append(s.messages, msg)
	s.byID[msg.ID] = msg

	// Kapasite aşıldıysa en eskiyi düşür.
	for len(s.messages) > s.capacity {
		oldest := s.messages[0]
		s.messages = s.messages[1:]
		delete(s.byID, oldest.ID)
	}

	subs := make([]chan *Message, 0, len(s.subs))
	for _, ch := range s.subs {
		subs = append(subs, ch)
	}
	s.mu.Unlock()

	for _, ch := range subs {
		// Bloklamıyoruz: yavaş bir abone yüzünden SMTP oturumu
		// beklememeli. Yetişemeyen abone bildirim kaçırır, posta yine
		// depoda.
		select {
		case ch <- msg:
		default:
		}
	}
}

// List, postaları en yeniden eskiye doğru döner.
func (s *Store) List() []Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Summary, 0, len(s.messages))
	for i := len(s.messages) - 1; i >= 0; i-- {
		out = append(out, s.messages[i].Summary())
	}
	return out
}

// Search, sorguyu içeren postaları en yeniden eskiye doğru döner.
//
// Arama gövdeyi de kapsıyor: geliştiricinin aradığı şey çoğu zaman
// konuda değil, gövdedeki bir sipariş numarası ya da doğrulama
// bağlantısı oluyor. Sorgu boşsa tüm liste döner.
//
// Büyük/küçük harf ayrımı yok. Türkçe'nin noktalı i'si yüzünden
// strings.ToLower yerine Unicode katlaması gerekiyor: "İPTAL" ile
// "iptal" aynı sorguya düşmeli.
func (s *Store) Search(query string) []Summary {
	query = foldForSearch(query)
	if query == "" {
		return s.List()
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Summary, 0, len(s.messages))
	for i := len(s.messages) - 1; i >= 0; i-- {
		if messageMatches(s.messages[i], query) {
			out = append(out, s.messages[i].Summary())
		}
	}
	return out
}

func messageMatches(m *Message, query string) bool {
	fields := []string{m.Subject, m.From, m.Text, m.HTML}
	fields = append(fields, m.To...)
	for _, a := range m.Attachments {
		fields = append(fields, a.Filename)
	}
	for _, f := range fields {
		if strings.Contains(foldForSearch(f), query) {
			return true
		}
	}
	return false
}

// foldForSearch, karşılaştırma için metni katlar.
//
// strings.ToLower Türkçe'de yetmiyor: "İ" (U+0130) küçültülünce
// "i̇" (i + birleşen nokta) oluyor ve "iptal" ile eşleşmiyor. Noktaları
// ayıklayıp katlıyoruz.
func foldForSearch(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch r {
		case 0x0307: // birleşen üst nokta
			continue
		case 'ı':
			r = 'i'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Get, kimliğe göre postayı döner.
func (s *Store) Get(id string) (*Message, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msg, ok := s.byID[id]
	return msg, ok
}

// Latest, en son gelen postayı döner.
//
// Testlerde en çok kullanılan çağrı: "az önce gönderilen posta ne oldu?"
func (s *Store) Latest() (*Message, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.messages) == 0 {
		return nil, false
	}
	return s.messages[len(s.messages)-1], true
}

// Count, saklanan posta sayısı.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.messages)
}

// Clear, tüm postaları siler.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = nil
	s.byID = make(map[string]*Message)
}

// Delete, tek bir postayı siler.
func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.byID[id]; !ok {
		return false
	}
	delete(s.byID, id)
	for i, msg := range s.messages {
		if msg.ID == id {
			s.messages = append(s.messages[:i], s.messages[i+1:]...)
			break
		}
	}
	return true
}

// Subscribe, yeni postaları alacak bir kanal döner. Dönen fonksiyon
// aboneliği sonlandırır.
func (s *Store) Subscribe() (<-chan *Message, func()) {
	ch := make(chan *Message, 16)

	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.subs[id] = ch
	s.mu.Unlock()

	return ch, func() {
		s.mu.Lock()
		if existing, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(existing)
		}
		s.mu.Unlock()
	}
}
