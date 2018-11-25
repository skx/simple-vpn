package shared

// MacAddr stores a MAC address.
type MacAddr [6]byte

// GetSrcMAC retreives the source MAC address of a TCP/IP packet.
func GetSrcMAC(packet []byte) MacAddr {
	var mac MacAddr
	copy(mac[:], packet[6:12])
	return mac
}

// GetDestMAC retreives the destination MAC address of a TCP/IP packet.
func GetDestMAC(packet []byte) MacAddr {
	var mac MacAddr
	copy(mac[:], packet[0:6])
	return mac
}

// MACIsUnicast returns true if the MAC address is a unicast address.
func MACIsUnicast(mac MacAddr) bool {
	return (mac[0] & 1) == 0
}
