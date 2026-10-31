package pki

import (
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"strings"
)

// words has 256 short, distinct words: one word per byte of the hash.
var words = strings.Fields(`
acid acorn actor adult agent alarm album alert alien alley amber angel
ankle apple april arena armor arrow atlas attic audio aunt award bacon
badge bagel baker bamboo banjo barn basin beach beard beast berry bike
bird bison blade blank blaze bloom board boat bolt bonus book boots
bottle brain brass bread brick bride brush bubble bucket buddy bunny
cabin cable cactus camel camera candy canoe canvas cargo carpet castle
cedar chain chalk chess chief chili cider cinema circle city clock cloud
coach cobra cocoa coffee comet coral cotton couch cowboy crab crane crown
cube cycle daisy dance delta denim desert diary dingo dog dolphin donut
door dragon drum duck eagle earth echo eclipse elbow elder ember engine
falcon farm feather fence ferry fiber field flame flute focus forest fox
frost fruit galaxy garden gecko ghost giant ginger glass globe goat gold
grape gravel guitar hammer harbor hawk hazel helmet hero honey horse
hotel igloo index iris island ivory jacket jaguar jelly jewel jungle
kayak kettle kiwi knight koala ladder lake lamp lava lemon lily lion
lizard llama lobster lotus magnet mango maple marble meadow melon metal
meteor mint mirror monkey moon moose motor mouse mule nectar needle
nest noodle oasis ocean olive onion orange orbit otter owl oyster paddle
panda paper parrot peach pearl pencil pepper piano pilot pine planet
plum pony poppy potato prism pumpkin puzzle quartz rabbit radar radio
raven river robot rocket ruby saddle salmon sand satin scarf shark
shell silver skate snail sofa spider spoon squid star stone storm sugar
tiger
`)

// Code returns the four words shown by gate.exe and in the panel for a
// device's public key. Both sides compute it, so comparing them proves the
// request in the panel came from that device.
func Code(pub crypto.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(der)
	return words[h[0]] + " " + words[h[1]] + " " + words[h[2]] + " " + words[h[3]]
}

func init() {
	if len(words) != 256 {
		panic("pki: code word list must have 256 words")
	}
}
