// Copyright 2019 HenryYee.
//
// Licensed under the AGPL, Version 3.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    https://www.gnu.org/licenses/agpl-3.0.en.html
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package lib

import (
	"Yearning-go/src/model"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"golang.org/x/crypto/pbkdf2"
	"log"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

func GetRandom() []byte {
	str := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	destr := []byte(str)
	result := []byte{}
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := 0; i < 12; i++ {
		result = append(result, destr[r.Intn(len(destr))])
	}
	return result
}

func DjangoEncrypt(password string, sl string) string {
	pwd := []byte(password)
	salt := []byte(sl)
	iterations := 120000
	digest := sha256.New
	dk := pbkdf2.Key(pwd, salt, iterations, 32, digest)
	str := base64.StdEncoding.EncodeToString(dk)
	return "pbkdf2_sha256" + "$" + strconv.FormatInt(int64(iterations), 10) + "$" + string(salt) + "$" + str
}

func DjangoCheckPassword(account *model.CoreAccount, password string) bool {
	sl := strings.Split(account.Password, "$")[2]
	checkPasswordToken := DjangoEncrypt(password, sl)
	if account.Password == checkPasswordToken {
		return true
	} else {
		return false
	}
}

func Encrypt(p string) string {

	if len(model.JWT) == 16 {
		// 转成字节数组
		origData := []byte(p)
		k := []byte(model.JWT)
		// 分组秘钥
		block, _ := aes.NewCipher(k)
		// 获取秘钥块的长度
		blockSize := block.BlockSize()
		// 补全码
		origData = PKCS7Padding(origData, blockSize)
		// 加密模式
		blockMode := cipher.NewCBCEncrypter(block, k[:blockSize])
		// 创建数组
		cryted := make([]byte, len(origData))
		// 加密
		blockMode.CryptBlocks(cryted, origData)

		return base64.StdEncoding.EncodeToString(cryted)
	}
	return ""
}

func Decrypt(cryted string) string {
	// 转成字节数组
	crytedByte, _ := base64.StdEncoding.DecodeString(cryted)
	k := []byte(model.JWT)

	// 分组秘钥
	block, _ := aes.NewCipher(k)
	// 获取秘钥块的长度
	blockSize := block.BlockSize()
	// 加密模式
	blockMode := cipher.NewCBCDecrypter(block, k[:blockSize])
	// 创建数组
	orig := make([]byte, len(crytedByte))
	// 解密
	blockMode.CryptBlocks(orig, crytedByte)
	// 去补全码
	orig = PKCS7UnPadding(orig)
	if orig == nil {
		log.Println("无法获得传入密码")
		return ""
	}
	return string(orig)
}

//补码
func PKCS7Padding(ciphertext []byte, blocksize int) []byte {
	padding := blocksize - len(ciphertext)%blocksize
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(ciphertext, padtext...)
}

//去码
func PKCS7UnPadding(origData []byte) []byte {
	if len(origData) > 0 {
		length := len(origData)
		unpadding := int(origData[length-1])
		return origData[:(length - unpadding)]
	}
	return nil
}

func hmacSha256(stringToSign string, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(stringToSign))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
