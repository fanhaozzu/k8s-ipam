package v1alpha1

import (
	"fmt"
	"math/rand"
	"math/big"
	"net"
	"time"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"github.com/apcera/util/iprange"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type IPPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []IPPool `json:"items"`
}

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type IPPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              IPPoolSpec   `json:"spec"`
	Status            IPPoolStatus `json:"status,omitempty"`
}

type IPPoolSpec struct {
	IPPoolSubs         []IPPoolSub	            `json:"ipPoolSubs"`
	StaticReservations IPReservationMap         `json:"staticReservations"`
}

type IPPoolSub struct {
	Range              string                   `json:"range"`
        NetmaskBits        int                      `json:"netmaskBits"`
        Gateway            net.IP                   `json:"gateway"`
        ReservedRanges     []string                 `json:"reservedRanges"`
}

type IPPoolStatus struct {
	DynamicReservations IPReservationMap
}

// GetIPPoolSub returns the IPPoolSub of podIP
func (p *IPPool) GetIPPoolSub(ip net.IP) IPPoolSub {
	ipPoolSub := IPPoolSub {Range: "NULL"}
        if len(p.Spec.IPPoolSubs) == 0 {
                return ipPoolSub
        }
	
	for _, ipPoolSub := range p.Spec.IPPoolSubs {
		if ipPoolSub.RangeContains(ip) {
			return ipPoolSub
		}
	}

	return ipPoolSub
}

// GetMask returns the netmask for ips allocated in this range
func (s *IPPoolSub) GetMask() net.IPMask {
	return net.CIDRMask(s.NetmaskBits, 32) //ipv4
}

func (s *IPPoolSub) IPRange() *iprange.IPRange {
	ipRange, _ := iprange.ParseIPRange(s.Range)
	return ipRange
}

// calculate the size of the range
func (s *IPPoolSub) IPRangeSize() int64 {
	ipRange := s.IPRange()
	startBig := big.NewInt(0)
	startBig.SetBytes(ipRange.Start)
	endBig := big.NewInt(0)
	endBig.SetBytes(ipRange.End)
	sizeBig := endBig.Sub(endBig, startBig)

	// 1 is added to the size because the end IP is inclusive
	return sizeBig.Int64() + 1
}

// RangeContains returns true if ip is within the range allocated from this pool
func (s *IPPoolSub) RangeContains(ip net.IP) bool {
	return s.IPRange().Contains(ip)
}

// ReservedRangeContains return true if ip is reserved in the pool
func (s *IPPoolSub) ReservedRangeContains(ip net.IP) bool {
	if s.Gateway.Equal(ip) {
		return true	
	}

	if len(s.ReservedRanges) == 0 {
		return false
	}
        for _, reservedRange := range s.ReservedRanges {
		reservedIpRange, _ := iprange.ParseIPRange(reservedRange)
		if reservedIpRange.Contains(ip) {
			return true
		}     
	}       
	return false 
}

// GetExistingReservation checks if a reservation for this pod exists, if so return the IP
func (p *IPPool) GetExistingReservation(namespace, podName string) *net.IP {
	if p.Spec.StaticReservations != nil {
		if staticIP := p.Spec.StaticReservations.GetExistingReservation(namespace, podName); staticIP != nil {
			return staticIP
		}
	}

	if p.Status.DynamicReservations == nil {
		return nil
	}
	return p.Status.DynamicReservations.GetExistingReservation(namespace, podName)
}

func (s *IPPoolSub) RandomIP() net.IP {
	var netIP net.IP
	ipRange := s.IPRange()
        startBig := big.NewInt(0)
        startBig.SetBytes(ipRange.Start)
        endBig := big.NewInt(0)
        endBig.SetBytes(ipRange.End)
        sizeBig := endBig.Sub(endBig, startBig)

        // 1 is added to the size because the end IP is inclusive
        ipRangeSize := sizeBig.Int64() + 1
	rand.Seed(time.Now().UnixNano())
	for netIP == nil {
		// get a random number within the size to start with
        	idx := rand.Int63n(ipRangeSize)
		startBig := big.NewInt(0)
        	startBig.SetBytes(ipRange.Start)
        	newBig := big.NewInt(0).Add(startBig, big.NewInt(idx))
        	ip := s.bigIntToIP(newBig)
		if !s.ReservedRangeContains(ip) {
			netIP = ip
			break
		}	
	}
	return netIP
}

func (s *IPPoolSub) bigIntToIP(newBig *big.Int) net.IP {
	// Convert it back into a 16 byte slice. net.IP expects a 16 byte
	// slice, and expects the elements to be not be the leading bytes
	// but the trailing. So we must create a new slice and populate its
	// tail.
	buf := newBig.Bytes()
	ipbytes := make([]byte, 16)
	position := 16 - len(buf)
	ipv6in4 := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff}
	// If the position we need to copy to is less than 0, then this
	// would cause an index out of range. This will only happen when
	// we've max'd out 16 bytes, so then we'll just loop around to zero.
	if position >= 0 {
		// copy only the last 4 bytes and ensure we set the IPv4 in v6 prefix
		copy(ipbytes, ipv6in4)
		copy(ipbytes[12:], buf[len(buf)-4:])
	}

	return net.IP(ipbytes)
}

// GetPodForIP returns the namespace and pod name for the pod associated with a reservation.  found is set to false if no pod is found.
func (p *IPPool) GetPodForIP(ip net.IP) (namespace, podName string, found bool) {
	if p.Spec.StaticReservations != nil {
		namespace, podName, found := p.Spec.StaticReservations.GetPodForIP(ip)
		if found {
			return namespace, podName, true
		}
	}

	if p.Status.DynamicReservations != nil {
		namespace, podName, found := p.Status.DynamicReservations.GetPodForIP(ip)
		if found {
			return namespace, podName, true
		}
	}

	return "", "", false
}

func (p *IPPool) Reserve(namespace, podName string, ip net.IP) {
	if p.Status.DynamicReservations == nil {
		p.Status.DynamicReservations = NewIPReservationMap()
	}
	p.Status.DynamicReservations.Reserve(namespace, podName, ip)
}

// FreeDynamicPodReservation removes any existing dynamic reservations for a given pod
func (p *IPPool) FreeDynamicPodReservation(namespace, podName string) {
	if p.Status.DynamicReservations == nil {
		return
	}

	p.Status.DynamicReservations.FreePodReservation(namespace, podName)
}

// Validate returns nil if there are no obvious errors in IP Pool configuration
func (s *IPPoolSub) Validate() error {
	// Range is valid
	_, err := iprange.ParseIPRange(s.Range)
	if err != nil {
		return fmt.Errorf("IP range is invalid (%v), please check your syntax: %v", s.Range, err)
	}
	// ReservedRange is valid
	if len(s.ReservedRanges) != 0 {
		for _, reservedRange := range s.ReservedRanges {
			_, err := iprange.ParseIPRange(reservedRange)
        		if err != nil {
                		return fmt.Errorf("Reserved IP range is invalid (%v), please check your syntax: %v", reservedRange, err)
        		}
		}
	}
	
	// NetmaskBits are valid
	if s.NetmaskBits <= 0 || s.NetmaskBits >= 32 {
		return fmt.Errorf("Specified netmask is invalid")
	}	

	if s.Gateway == nil  {
		return fmt.Errorf("Gateway must be set.")
	}

	return nil
}

type IPReservationMap map[string]map[string]net.IP

func NewIPReservationMap() IPReservationMap {
	return make(map[string]map[string]net.IP)
}

func (m IPReservationMap) GetExistingReservation(namespace, podName string) *net.IP {
	if namespaceMap, nsFound := m[namespace]; nsFound {
		if podIp, podFound := namespaceMap[podName]; podFound {
			return &podIp
		}
	}
	return nil
}

func (m IPReservationMap) GetPodForIP(ip net.IP) (namespace, podName string, found bool) {
	for namespace, nsMap := range m {
		for podName, podIp := range nsMap {
			if podIp.Equal(ip) {
				return namespace, podName, true
			}
		}
	}
	return "", "", false
}

func (m IPReservationMap) Reserve(namespace, podName string, ip net.IP) {
	if _, ok := m[namespace]; !ok {
		m[namespace] = make(map[string]net.IP, 0)
	}
	m[namespace][podName] = ip
}

func (m IPReservationMap) AlreadyReserved(ip net.IP) bool {
	_, _, found := m.GetPodForIP(ip)
	return found
}

func (m IPReservationMap) FreePodReservation(namespace, podName string) {
	if _, nsFound := m[namespace]; nsFound {
		if _, podFound := m[namespace][podName]; podFound {
			delete(m[namespace], podName)
		}

		if len(m[namespace]) == 0 {
			delete(m, namespace)
		}
	}
}
