/*
 * SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package step

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Idempotence", func() {
	It("accepts the recognised values", func() {
		Expect(Auto.Validate()).To(Succeed())
		Expect(Disabled.Validate()).To(Succeed())
	})

	It("rejects unrecognised values", func() {
		Expect(Idempotence("bad").Validate()).To(MatchError("bad is not a valid idempotence value"))
	})

	It("marshals only recognised values", func() {
		auto, err := Auto.MarshalJSON()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(auto)).To(Equal("false"))

		disabled, err := Disabled.MarshalJSON()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(disabled)).To(Equal("true"))

		_, err = Idempotence("bad").MarshalJSON()
		Expect(err).To(MatchError(ContainSubstring("bad is not a valid idempotence value")))
	})

	It("unmarshals supported boolean values", func() {
		var value Idempotence
		Expect(json.Unmarshal([]byte("true"), &value)).To(Succeed())
		Expect(value).To(Equal(Disabled))

		Expect(json.Unmarshal([]byte("false"), &value)).To(Succeed())
		Expect(value).To(Equal(Auto))
	})

	It("rejects null and invalid JSON values without changing the receiver", func() {
		for _, data := range []string{"null", `"auto"`, "1", "{"} {
			value := Disabled
			Expect(json.Unmarshal([]byte(data), &value)).To(HaveOccurred(), data)
			Expect(value).To(Equal(Disabled), data)
		}
	})
})
