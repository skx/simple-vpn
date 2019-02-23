package shared

// MacAddr stores a MAC address.
type MacAddr [6]byte

// GetSrcMAC retrieves the source MAC address of a TCP/IP packet.
func GetSrcMAC(packet []byte) MacAddr {
	var mac MacAddr
	copy(mac[:], packet[16:32])
	return mac
}

// GetDestMAC retreives the destination MAC address of a TCP/IP packet.
func GetDestMAC(packet []byte) MacAddr {
	var mac MacAddr
	copy(mac[:], packet[0:16])
	return mac
}

// MACIsUnicast returns true if the MAC address is a unicast address.
func MACIsUnicast(mac MacAddr) bool {
	return (mac[0] & 1) == 0
}
