# 素材索引（grammar.index.md）

> 每个条目描述一个讲解 MD：lang / path / tags / level / hash。
> hash 由 `sha256sum` 计算，脚本按实际文件内容重算，不一致时会告警。

## 德语

- lang:de, path:de/grammar/不占位连词.md, patterns:(?i)\b(und|oder|aber|denn|sondern)\b, prompts:Ich mag Kaffee aber lieber Tee.|Möchten Sie Tee oder Kaffee?|Ich bleibe zu Hause denn es regnet., tags:conjunction,main-clause,word-order, level:A2, hash:sha256:6abfbeb480d7368bc61cefef8bb0d254f382eac1cbaa7bc0783989f17488193c
- lang:de, path:de/grammar/不定冠词.md, patterns:(?i)\b(ein|eine|einen|einem|einer|eines)\b, prompts:Was ist das? — Das ist ein Tisch.|Ich möchte einen Kaffee.|Was sind Sie von Beruf?, tags:indefinite,article, level:A2, hash:sha256:0929d37f335e4cb2efbf09f37cc39724c4796808d76c3d4ba32048bdbdc21e89
- lang:de, path:de/grammar/人称代词.md, patterns:(?i)\b(ich|du|er|sie|es|wir|ihr)\b, prompts:Wie heißen Sie?|Woher kommen Sie?|Was machen Sie gern?, tags:personal,pronoun, level:A2-B1, hash:sha256:2f554b5ebadfa06a719cee08b72d0771d0faeaef9357177a4ff97a15a9146bec
- lang:de, path:de/grammar/占位连词与从句语序.md, patterns:(?i)\b(weil|dass|ob|wenn|als|obwohl|damit|während|bevor|nachdem)\b, prompts:Warum lernen Sie Deutsch?|Was machen Sie wenn Sie Zeit haben?|Ich glaube dass Deutsch wichtig ist., tags:conjunction,subordinate-clause,word-order, level:A2-B1, hash:sha256:331394fb7f1ab65f3d7d7c54dc4af8627e0d9303bba4c41995412c7c18579624
- lang:de, path:de/grammar/只跟第三格的介词.md, patterns:(?i)\b(aus|bei|mit|nach|seit|von|zu|gegenüber)\b, prompts:Woher kommen Sie?|Mit wem fahren Sie in den Urlaub?|Wie kommen Sie zur Arbeit?, tags:preposition,dative, level:A2-B1, hash:sha256:b08f9edd3fd2b688cbcc5e14e4d2a7babaa0e2527baad3c6fdf575c6bab300e4
- lang:de, path:de/grammar/只跟第二格的介词.md, patterns:(?i)\b(wegen|trotz|während|statt|innerhalb|außerhalb|aufgrund)\b, prompts:Warum fliegen Sie nicht? — Wegen der Umwelt.|Trotz des Regens gehe ich spazieren.|Während der Arbeit höre ich Musik., tags:preposition,genitive, level:B1, hash:sha256:ba101c0577f51c01e0f67a8082014532ecb1a549fea8e2446ce51978c9e6a18f
- lang:de, path:de/grammar/只跟第四格的介词.md, patterns:(?i)\b(durch|für|gegen|ohne|um|bis|entlang)\b, prompts:Was machen Sie für das Wochenende?|Fahren Sie durch die Stadt?|Kaufen Sie ein Geschenk für Ihren Freund?, tags:preposition,accusative, level:A2, hash:sha256:e1b4f3a8ee9cdb6cfa2ae242aa26e28b5cd474684ad1b9febfcc4a6988b11d21
- lang:de, path:de/grammar/名词复数第三格词尾.md, patterns:(?i)\b(den|denen)\s+\w+(en|n)\b, prompts:Mit wem wohnen Sie zusammen?|Reisen Sie mit Freunden?|Sprechen Sie oft mit den Nachbarn?, tags:noun,dative,plural,ending, level:A2, hash:sha256:cdf89fa4ba9b2aa9872ce9479ba39b634dff8f4799dae1244b643b44035a54cb
- lang:de, path:de/grammar/定冠词.md, patterns:(?i)\b(der|die|das|den|dem|des)\b, prompts:Was ist das? — Das ist der Tisch.|Wo ist der Supermarkt?|Wie gefällt Ihnen die Wohnung?, tags:definite,article, level:A2, hash:sha256:8a6d64c3c1b7038704840d75dd9521f1291043a9a22eceb204dde673dc60cfdb
- lang:de, path:de/grammar/形容词词尾变化.md, patterns:(?i)\b(der|die|das|den|dem|ein|eine|einen|einem|einer)\s+\w+(e|en|er|es|em)\b, prompts:Beschreiben Sie Ihre Wohnung: die helle Küche und der große Balkon.|Wie ist das Wetter heute?|Welche Medien nutzen Sie?, tags:adjective,ending,declension, level:A2-B1, hash:sha256:56c7b33614039d1a5bb72407a5b94856fec88da5ca6bda85f44c2e384e752df1
- lang:de, path:de/grammar/物主代词.md, patterns:(?i)\b(mein|dein|sein|ihr|unser|euer)\w*\b, prompts:Wie heißen Sie? — Mein Name ist ...|Wie ist Ihre Wohnung?|Was ist Ihr Hobby?, tags:possessive,pronoun,case,nominativ,dativ,akkusativ, level:A2, hash:sha256:8e8414139991b46bbdd63c2358ca81502cf6d82e261ba10787e415eda5307f22
- lang:de, path:de/grammar/真反身动词.md, patterns:(?i)\bsich\b, prompts:Wofür interessieren Sie sich?|Freuen Sie sich auf das Wochenende?|Treffen Sie sich oft mit Freunden?, tags:reflexive,verb,sich, level:A2-B1, hash:sha256:b4d547843e85169257630acbac0e3b7108054f23555cc96dba8703d948ca836f
- lang:de, path:de/grammar/第二分词构成规则.md, patterns:(?i)\bge\w+(t|en)\b, prompts:Was haben Sie am Wochenende gemacht?|Sind Sie schon einmal in Berlin gewesen?|Ich habe einen Termin vereinbart., tags:verb,participle,perfekt, level:A2-B1, hash:sha256:6fcb9f79c4eddc5232ffb84e8e9c153b67443df8a09152f70f509c43599fe968
- lang:de, path:de/grammar/第二虚拟式.md, patterns:(?i)\b(würde|würden|wäre|wären|hätte|hätten|könnte|könnten|müsste|müssten|sollte|sollten|dürfte|dürften)\b, prompts:Was würden Sie machen wenn Sie mehr Zeit hätten?|Wäre das für Sie in Ordnung?|Sollten alle im Homeoffice arbeiten?, tags:konjunktiv,irreal,polite, level:B1, hash:sha256:b560e3a248ab50e5fd8c48c473387aebd5a86f5ff3fe385df0bcd22f013d7eeb
- lang:de, path:de/grammar/过去时变位规则.md, patterns:(?i)\b\w+(te|test|ten|tet)\b, prompts:Wie war Ihre letzte Reise?|Wo waren Sie im Urlaub?|Früher hatte ich mehr Zeit., tags:verb,preterite,tense, level:B1, hash:sha256:7bde5ab0bda34f4214f14ddd605f651ab8155f6f407bc2aa40e24f8f8a2924b7
- lang:de, path:de/grammar/高频固定搭配.md, patterns:(?i)\b(warten auf|denken an|träumen von|sich freuen auf|sich freuen über)\b,(?i)\b(Angst haben vor|Interesse haben an|teilnehmen an|sich erinnern an|sich vorbereiten auf)\b,(?i)\b(wichtig für|gut in|zufrieden mit|interessiert an|bereit für|fertig mit)\b,(?i)\b(Lust haben auf|Zeit haben für|Hunger haben nach|Durst haben nach)\b, prompts:Worauf freuen Sie sich?|Interessieren Sie sich für Sport?|Haben Sie Lust auf einen Ausflug?, tags:collocation,preposition,set-phrase, level:A2-B1, hash:sha256:5a007fd4b50b5f1525e7761599f3b2fa6616ab1da4f762f00d7502a08b422998
- lang:de, path:de/vocab/Hund.md, tags:noun,plural, level:A2, hash:sha256:a89b0d1905d5d71b16eff390d686e02bd7cd4c507c8e0c9692ceefb8025156d8
- lang:de, path:de/vocab/helfen.md, tags:verb,case, level:A2, hash:sha256:0bae3e56cbb2f94383f4882b5e7a0fbfddbf8e729bebe4dbb28660f47751357b
- lang:de, path:de/vocab/Tisch.md, tags:noun,plural, level:A1, hash:sha256:f93eb5aaccdf183a8ce232b363ad33c868066fd690e75680d2b76962314ea7c3

## 英语

- lang:en, path:en/grammar/人称代词.md, tags:pronoun,case, level:A1, hash:sha256:9109707df276a1d49cf5a1b48206c38eafb427ea308beb8fbb964f213541cd1a
- lang:en, path:en/vocab/help.md, tags:verb,collocation, level:A2, hash:sha256:9614b6197b40186bacbca374f81e07e6498c7dc10674a337fba08ff1e0f4dcd3
- lang:en, path:en/vocab/go.md, tags:verb,tense, level:A1, hash:sha256:9720b393973466f17dda838c435e804d8571716baec6367963df396ea7561350
- lang:en, path:en/vocab/book.md, tags:noun,plural, level:A1, hash:sha256:7dfced1737e1261739f4d7cb546a4a2fa885ce6f589fb88ac16a6796784a18aa

## 日语

- lang:ja, path:ja/grammar/助词はとが.md, tags:particle,contrast, level:N5, hash:sha256:02e60174cd38766eb7e428f9b9999fb7a345772a56acbb3d575961d37d7d15dd
- lang:ja, path:ja/vocab/山.md, tags:noun,kanji, level:N5, hash:sha256:3c48ff9b33a59db53aabc985cb3418e5e92f3ef94655eb0347a39d5ee74cc030
- lang:ja, path:ja/vocab/行く.md, tags:verb,conjugation, level:N5, hash:sha256:02201851aea2a7528e31f6cec921e66a578256781158ee35edb8b18cbc86dc80
